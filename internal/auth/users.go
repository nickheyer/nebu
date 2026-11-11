package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxUsername = 64
	// Failed sign-ins allowed per account and per client address inside the window
	userAttempts   = 10
	clientAttempts = 50
	attemptWindow  = 15 * time.Minute
	// Prune the attempt table once it holds this many keys
	attemptKeys = 1000
)

var (
	// Invalid username or password
	ErrUser = errors.New("account")
	// No such account
	ErrUnknownUser = errors.New("unknown account")
	// Sign-in refused
	ErrCredentials = errors.New("wrong username or password")
	// Too many failed sign-ins
	ErrThrottled = errors.New("too many failed sign-ins, wait a few minutes")
	// Refuses to remove the only account
	ErrLastUser = errors.New("the last account cannot be removed, set auth.disabled to turn accounts off")
	// Refuses removing the caller's own account
	ErrOwnAccount = errors.New("sign in as another account to remove this one")
	// Refuses taking the admin role from the only admin
	ErrLastAdmin = errors.New("the last admin keeps the admin role")
	// Refuses setup once any account exists
	ErrSetupDone = errors.New("an account already exists, sign in instead")
	// Usernames may hold letters, digits, and ._@+-
	usernameExtra = "._@+-"
)

// Verified against when the account does not exist, so unknown names take as long as wrong passwords
var dummyHash = mustHash("nebu-dummy-password")

func mustHash(pw string) string {
	h, err := HashPassword(pw)
	if err != nil {
		panic(err)
	}
	return h
}

// Failed sign-ins for one key inside the current window
type attempts struct {
	count int
	since time.Time
}

// What session checks need of one account
type account struct {
	// Sessions issued before the last password change are revoked
	changed time.Time
	role    v1.Role
}

// Local accounts, with sign-in throttling and session revocation
type Users struct {
	db  *db.DB
	log *slog.Logger
	now func() time.Time
	// Where the accounts' API tokens are cached, told when an account is removed
	tokens *Tokens

	mu       sync.Mutex
	accounts map[string]account
	failures map[string]*attempts
}

// Accounts over the store, their API tokens cached in the table given
func NewUsers(store *db.DB, log *slog.Logger, tokens *Tokens) *Users {
	return &Users{db: store, log: log, now: time.Now, tokens: tokens, accounts: map[string]account{}, failures: map[string]*attempts{}}
}

// Reads every account's password change time and role for session checks
func (u *Users) Load(ctx context.Context) error {
	rows, err := u.db.ListUsers(ctx)
	if err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.accounts = make(map[string]account, len(rows))
	for _, r := range rows {
		u.accounts[r.User.GetUsername()] = account{changed: r.User.GetUpdatedAt().AsTime(), role: r.User.GetRole()}
	}
	return nil
}

// Whether a local session belongs to an account that still exists with the same password and role
func (u *Users) Valid(sess *Session) bool {
	if sess.Provider != ProviderLocal {
		return true
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	a, ok := u.accounts[sess.Name]
	return ok && !sess.Issued.Before(a.changed) && sess.Role == a.role
}

// Number of accounts
func (u *Users) Count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.accounts)
}

func (u *Users) List(ctx context.Context) ([]*v1.User, error) {
	rows, err := u.db.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*v1.User, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.User)
	}
	return out, nil
}

// Creates the first account as an admin in one transaction, refusing once any account exists
func (u *Users) Setup(ctx context.Context, username, password string) (*v1.User, error) {
	user, hash, err := u.newUser(username, password, v1.Role_ROLE_ADMIN)
	if err != nil {
		return nil, err
	}
	if err := u.db.CreateFirstUser(ctx, user, hash); errors.Is(err, db.ErrAccountsExist) {
		return nil, ErrSetupDone
	} else if err != nil {
		return nil, err
	}
	u.remember(user)
	u.log.Info("first account created", "username", user.GetUsername(), "role", RoleName(user.GetRole()))
	return user, nil
}

// Adds an account for an admin, the first always an admin and later ones in the role named
func (u *Users) Create(ctx context.Context, caller Identity, username, password string, role v1.Role) (*v1.User, error) {
	if !caller.Admin() {
		return nil, fmt.Errorf("%w: only an admin adds accounts", ErrForbidden)
	}
	if u.Count() == 0 {
		if role != v1.Role_ROLE_UNSPECIFIED && role != v1.Role_ROLE_ADMIN {
			return nil, fmt.Errorf("%w: the first account is an admin, leave the role unset or choose admin", ErrUser)
		}
		user, err := u.Setup(ctx, username, password)
		if !errors.Is(err, ErrSetupDone) {
			return user, err
		}
	}
	if _, known := v1.Role_name[int32(role)]; !known || role == v1.Role_ROLE_UNSPECIFIED {
		return nil, fmt.Errorf("%w: role is required, admin, operator, or inference", ErrUser)
	}
	user, hash, err := u.newUser(username, password, role)
	if err != nil {
		return nil, err
	}
	if _, err := u.db.GetUser(ctx, user.GetUsername()); err == nil {
		return nil, fmt.Errorf("%w: %s already exists", ErrUser, user.GetUsername())
	} else if !db.IsNotFound(err) {
		return nil, err
	}
	if err := u.db.PutUser(ctx, user, hash); err != nil {
		return nil, err
	}
	u.remember(user)
	u.log.Info("account created", "username", user.GetUsername(), "role", RoleName(role))
	return user, nil
}

// Checks the name and password and builds the account row with its hash
func (u *Users) newUser(username, password string, role v1.Role) (*v1.User, string, error) {
	name, err := normalize(username)
	if err != nil {
		return nil, "", err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, "", err
	}
	id, err := RandomString(12)
	if err != nil {
		return nil, "", err
	}
	now := u.now().UTC()
	return &v1.User{Id: id, Username: name, Role: role, CreatedAt: timestamppb.New(now), UpdatedAt: timestamppb.New(now)}, hash, nil
}

// Records a new account for session checks
func (u *Users) remember(user *v1.User) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.accounts[user.GetUsername()] = account{changed: user.GetUpdatedAt().AsTime(), role: user.GetRole()}
}

// Removes an account for an admin, revoking its sessions and the API tokens it made
func (u *Users) Delete(ctx context.Context, caller Identity, username string) error {
	if !caller.Admin() {
		return fmt.Errorf("%w: only an admin removes accounts", ErrForbidden)
	}
	name, err := normalize(username)
	if err != nil {
		return err
	}
	if caller.Provider == ProviderLocal && SameUser(caller.Name, name) {
		return ErrOwnAccount
	}
	u.mu.Lock()
	_, exists := u.accounts[name]
	last := len(u.accounts) == 1
	u.mu.Unlock()
	if !exists {
		return fmt.Errorf("%w: %s", ErrUnknownUser, name)
	}
	if last {
		return ErrLastUser
	}
	id, err := u.db.DeleteUser(ctx, name)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("%w: %s", ErrUnknownUser, name)
	}
	u.mu.Lock()
	delete(u.accounts, name)
	u.mu.Unlock()
	u.tokens.ForgetAccount(ProviderLocal, id)
	u.log.Info("account removed", "username", name)
	return nil
}

// Gives an account another role for an admin, ending its sessions and moving its API tokens to the role
func (u *Users) SetRole(ctx context.Context, caller Identity, username string, role v1.Role) (*v1.User, error) {
	if !caller.Admin() {
		return nil, fmt.Errorf("%w: only an admin changes roles", ErrForbidden)
	}
	name, err := normalize(username)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	target, exists := u.accounts[name]
	admins := 0
	for _, a := range u.accounts {
		if a.role == v1.Role_ROLE_ADMIN {
			admins++
		}
	}
	u.mu.Unlock()
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrUnknownUser, name)
	}
	if target.role == v1.Role_ROLE_ADMIN && role != v1.Role_ROLE_ADMIN && admins == 1 {
		return nil, ErrLastAdmin
	}
	id, err := u.db.SetUserRole(ctx, ProviderLocal, name, role)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("%w: %s", ErrUnknownUser, name)
	}
	u.mu.Lock()
	target = u.accounts[name]
	target.role = role
	u.accounts[name] = target
	u.mu.Unlock()
	u.tokens.ForgetAccount(ProviderLocal, id)
	row, err := u.db.GetUser(ctx, name)
	if err != nil {
		return nil, err
	}
	u.log.Info("role changed", "username", name, "role", role, "by", caller.Name)
	return row.User, nil
}

// Replaces a password for its owner or an admin, local callers proving their own current one, and revokes older sessions
func (u *Users) SetPassword(ctx context.Context, caller Identity, username, current, password string) (*v1.User, error) {
	name, err := normalize(username)
	if err != nil {
		return nil, err
	}
	own := caller.Provider == ProviderLocal && SameUser(caller.Name, name)
	if !own && !caller.Admin() {
		return nil, fmt.Errorf("%w: only an admin changes another account's password", ErrForbidden)
	}
	if caller.Provider == ProviderLocal {
		if _, err := u.Verify(ctx, caller.Name, current); errors.Is(err, ErrCredentials) {
			return nil, fmt.Errorf("%w: current_password must be your own current password", ErrCredentials)
		} else if err != nil {
			return nil, err
		}
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	now := u.now().UTC()
	ok, err := u.db.SetUserPassword(ctx, name, hash, now)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownUser, name)
	}
	u.mu.Lock()
	a := u.accounts[name]
	a.changed = now
	u.accounts[name] = a
	u.mu.Unlock()
	row, err := u.db.GetUser(ctx, name)
	if err != nil {
		return nil, err
	}
	u.log.Info("password changed", "username", name, "by", caller.Name)
	return row.User, nil
}

// Checks a password without throttling, for callers already signed in
func (u *Users) Verify(ctx context.Context, username, password string) (*v1.User, error) {
	name, err := normalize(username)
	if err != nil {
		return nil, ErrCredentials
	}
	row, err := u.db.GetUser(ctx, name)
	if db.IsNotFound(err) {
		VerifyPassword(dummyHash, password)
		return nil, ErrCredentials
	}
	if err != nil {
		return nil, err
	}
	if !VerifyPassword(row.Hash, password) {
		return nil, ErrCredentials
	}
	return row.User, nil
}

// Signs in, refusing accounts and clients with too many recent failures
func (u *Users) Login(ctx context.Context, client, username, password string) (*v1.User, error) {
	name := strings.ToLower(strings.TrimSpace(username))
	userKey, clientKey := "u:"+name, "c:"+client
	if u.throttled(userKey, userAttempts) || u.throttled(clientKey, clientAttempts) {
		u.log.Warn("sign-in throttled", "username", name, "client", client)
		return nil, ErrThrottled
	}
	user, err := u.Verify(ctx, name, password)
	if errors.Is(err, ErrCredentials) {
		u.failed(userKey)
		u.failed(clientKey)
		u.log.Warn("sign-in refused", "username", name, "client", client)
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	delete(u.failures, userKey)
	delete(u.failures, clientKey)
	u.mu.Unlock()
	u.log.Info("signed in", "username", name, "client", client)
	return user, nil
}

func (u *Users) throttled(key string, limit int) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	a, ok := u.failures[key]
	if !ok {
		return false
	}
	if u.now().Sub(a.since) > attemptWindow {
		delete(u.failures, key)
		return false
	}
	return a.count >= limit
}

func (u *Users) failed(key string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	if len(u.failures) >= attemptKeys {
		for k, a := range u.failures {
			if now.Sub(a.since) > attemptWindow {
				delete(u.failures, k)
			}
		}
	}
	a, ok := u.failures[key]
	if !ok || now.Sub(a.since) > attemptWindow {
		u.failures[key] = &attempts{count: 1, since: now}
		return
	}
	a.count++
}

// Lowercases and checks a username
func normalize(username string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(username))
	if name == "" {
		return "", fmt.Errorf("%w: username is required", ErrUser)
	}
	if len(name) > maxUsername {
		return "", fmt.Errorf("%w: username is longer than %d characters", ErrUser, maxUsername)
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(usernameExtra, r) {
			return "", fmt.Errorf("%w: username may hold letters, digits, and %s", ErrUser, usernameExtra)
		}
	}
	return name, nil
}

// Whether two usernames name the same account
func SameUser(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
