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

// Local accounts, with sign-in throttling and session revocation
type Users struct {
	db  *db.DB
	log *slog.Logger
	now func() time.Time

	mu sync.Mutex
	// Password change times by username. Sessions issued earlier are revoked.
	accounts map[string]time.Time
	failures map[string]*attempts
}

func NewUsers(store *db.DB, log *slog.Logger) *Users {
	return &Users{db: store, log: log, now: time.Now, accounts: map[string]time.Time{}, failures: map[string]*attempts{}}
}

// Reads every account's password change time for session checks
func (u *Users) Load(ctx context.Context) error {
	rows, err := u.db.ListUsers(ctx)
	if err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.accounts = make(map[string]time.Time, len(rows))
	for _, r := range rows {
		u.accounts[r.User.GetUsername()] = r.User.GetUpdatedAt().AsTime()
	}
	return nil
}

// Whether a local session belongs to an account that still exists with the same password
func (u *Users) Valid(sess *Session) bool {
	if sess.Provider != ProviderLocal {
		return true
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	changed, ok := u.accounts[sess.Name]
	return ok && !sess.Issued.Before(changed)
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

// Adds an account
func (u *Users) Create(ctx context.Context, username, password string) (*v1.User, error) {
	name, err := normalize(username)
	if err != nil {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	id, err := RandomString(12)
	if err != nil {
		return nil, err
	}
	now := u.now().UTC()
	user := &v1.User{Id: id, Username: name, CreatedAt: timestamppb.New(now), UpdatedAt: timestamppb.New(now)}
	if _, err := u.db.GetUser(ctx, name); err == nil {
		return nil, fmt.Errorf("%w: %s already exists", ErrUser, name)
	} else if !db.IsNotFound(err) {
		return nil, err
	}
	if err := u.db.PutUser(ctx, user, hash); err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.accounts[name] = now
	u.mu.Unlock()
	u.log.Info("account created", "username", name)
	return user, nil
}

// Removes an account, revoking its sessions and the API tokens it made
func (u *Users) Delete(ctx context.Context, username string) error {
	name, err := normalize(username)
	if err != nil {
		return err
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
	if ok, err := u.db.DeleteUser(ctx, name); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownUser, name)
	}
	u.mu.Lock()
	delete(u.accounts, name)
	u.mu.Unlock()
	u.log.Info("account removed", "username", name)
	return nil
}

// Replaces a password and revokes sessions issued before the change
func (u *Users) SetPassword(ctx context.Context, username, password string) (*v1.User, error) {
	name, err := normalize(username)
	if err != nil {
		return nil, err
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
	u.accounts[name] = now
	u.mu.Unlock()
	row, err := u.db.GetUser(ctx, name)
	if err != nil {
		return nil, err
	}
	u.log.Info("password changed", "username", name)
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
