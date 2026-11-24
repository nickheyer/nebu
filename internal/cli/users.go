package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/auth"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/term"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func userCommands() command {
	return command{name: "users", summary: "local accounts for the web UI", run: runUsersList, local: true, sub: []command{
		{name: "list", summary: "list accounts", run: runUsersList, local: true},
		{name: "add", summary: "add an account", run: runUsersAdd, local: true},
		{name: "remove", summary: "remove an account", run: runUsersRemove, local: true},
		{name: "passwd", summary: "change an account's password", run: runUsersPasswd, local: true},
		{name: "role", summary: "change an account's role", run: runUsersRole, local: true},
	}}
}

func runUsersRole(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("users role"), args, 2, 2, "users role <username> <admin|operator|inference>")
	if err != nil {
		return err
	}
	role, err := parseRole(positional[1])
	if err != nil {
		return err
	}
	resp, err := e.cl.auth.SetRole(ctx, connect.NewRequest(&v1.SetRoleRequest{Username: positional[0], Role: role}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "%s is now %s\n", resp.Msg.GetUser().GetUsername(), auth.RoleName(resp.Msg.GetUser().GetRole()))
	})
}

func runUsersList(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("users list"), args, 0, 0, "users list"); err != nil {
		return err
	}
	resp, err := e.cl.auth.ListUsers(ctx, connect.NewRequest(&v1.ListUsersRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, u := range resp.Msg.GetUsers() {
			rows = append(rows, []string{u.GetUsername(), auth.RoleName(u.GetRole()), u.GetCreatedAt().AsTime().Local().Format("2006-01-02 15:04"), u.GetUpdatedAt().AsTime().Local().Format("2006-01-02 15:04")})
		}
		table(w, []string{"USERNAME", "ROLE", "CREATED", "PASSWORD CHANGED"}, rows)
	})
}

func runUsersAdd(ctx context.Context, e *env, args []string) error {
	fs := e.flags("users add")
	password := fs.String("password", "", "the password, prompted for when unset")
	stdin := fs.Bool("password-stdin", false, "read the password from standard input")
	roleName := fs.String("role", "", "admin, operator, or inference, required after the first account, which is always admin")
	positional, err := e.parse(fs, args, 1, 1, "users add <username> [--role R] [--password P | --password-stdin]")
	if err != nil {
		return err
	}
	role, err := parseRole(*roleName)
	if err != nil {
		return err
	}
	pw, err := e.password(*password, *stdin, true)
	if err != nil {
		return err
	}
	resp, err := e.cl.auth.CreateUser(ctx, connect.NewRequest(&v1.CreateUserRequest{Username: positional[0], Password: pw, Role: role}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "added %s as %s\n", resp.Msg.GetUser().GetUsername(), auth.RoleName(resp.Msg.GetUser().GetRole()))
	})
}

// The role a flag names, unspecified when empty since the daemon makes the first account an admin
func parseRole(name string) (v1.Role, error) {
	if strings.TrimSpace(name) == "" {
		return v1.Role_ROLE_UNSPECIFIED, nil
	}
	return auth.ParseRole(name)
}

func runUsersRemove(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("users remove"), args, 1, 1, "users remove <username>")
	if err != nil {
		return err
	}
	if _, err := e.cl.auth.DeleteUser(ctx, connect.NewRequest(&v1.DeleteUserRequest{Username: positional[0]})); err != nil {
		return err
	}
	e.text("removed %s\n", positional[0])
	return nil
}

func runUsersPasswd(ctx context.Context, e *env, args []string) error {
	fs := e.flags("users passwd")
	password := fs.String("password", "", "the new password, prompted for when unset")
	stdin := fs.Bool("password-stdin", false, "read the new password from standard input")
	positional, err := e.parse(fs, args, 1, 1, "users passwd <username> [--password P | --password-stdin]")
	if err != nil {
		return err
	}
	pw, err := e.password(*password, *stdin, true)
	if err != nil {
		return err
	}
	resp, err := e.cl.auth.SetPassword(ctx, connect.NewRequest(&v1.SetPasswordRequest{Username: positional[0], Password: pw}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "password changed for %s\n", resp.Msg.GetUser().GetUsername()) })
}

func tokenCommands() command {
	return command{name: "tokens", summary: "API tokens of a signed-in account", run: runTokensList, local: true, sub: []command{
		{name: "list", summary: "list the account's tokens with their scopes", run: runTokensList, local: true},
		{name: "create", summary: "make a token holding the scopes given", run: runTokensCreate, local: true},
		{name: "remove", summary: "revoke a token", run: runTokensRemove, local: true},
	}}
}

// Defines sign-in flags and returns a function signing in as the account --user names
func accountFlags(e *env, fs *flag.FlagSet) func(context.Context) error {
	user := fs.String("user", "", "local account to act as, signed in with its password, else the account of the configured API token")
	password := fs.String("password", "", "the account's password, prompted for when unset")
	stdin := fs.Bool("password-stdin", false, "read the account's password from standard input")
	return func(ctx context.Context) error {
		if *user == "" {
			if *password != "" || *stdin {
				return errors.New("--password and --password-stdin sign in as the account --user names, pass --user too")
			}
			return nil
		}
		pw, err := e.password(*password, *stdin, false)
		if err != nil {
			return err
		}
		return e.signIn(ctx, *user, pw)
	}
}

// Reads comma separated scope names, refusing any that is not inference, read, operate, or admin
func parseScopes(s string) ([]v1.Scope, error) {
	var out []v1.Scope
	for _, name := range strings.Split(s, ",") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		n, err := enumNamed(v1.Scope(0).Descriptor(), name, "scope")
		if err != nil {
			return nil, err
		}
		out = append(out, v1.Scope(n))
	}
	if len(out) == 0 {
		return nil, errors.New("--scopes names no scope, pass inference, read, operate, or admin separated by commas")
	}
	return out, nil
}

func scopesText(scopes []v1.Scope) string {
	names := make([]string, len(scopes))
	for i, sc := range scopes {
		names[i] = auth.ScopeName(sc)
	}
	return orDash(strings.Join(names, ","))
}

const tokenSignIn = "[--user NAME [--password P | --password-stdin]]"

func runTokensList(ctx context.Context, e *env, args []string) error {
	fs := e.flags("tokens list")
	signIn := accountFlags(e, fs)
	if _, err := e.parse(fs, args, 0, 0, "tokens list "+tokenSignIn); err != nil {
		return err
	}
	if err := signIn(ctx); err != nil {
		return err
	}
	resp, err := e.cl.auth.ListTokens(ctx, connect.NewRequest(&v1.ListTokensRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, t := range resp.Msg.GetTokens() {
			rows = append(rows, []string{t.GetId(), t.GetName(), t.GetPrefix(), scopesText(t.GetScopes()), when(t.GetCreatedAt(), "2006-01-02 15:04"), when(t.GetLastUsedAt(), "2006-01-02 15:04")})
		}
		table(w, []string{"ID", "NAME", "PREFIX", "SCOPES", "CREATED", "LAST USED"}, rows)
	})
}

func runTokensCreate(ctx context.Context, e *env, args []string) error {
	fs := e.flags("tokens create")
	scopes := fs.String("scopes", "", "comma separated scopes the token holds, of inference, read, operate, and admin")
	signIn := accountFlags(e, fs)
	positional, err := e.parse(fs, args, 1, 1, "tokens create <name> --scopes inference,read,operate,admin "+tokenSignIn)
	if err != nil {
		return err
	}
	list, err := parseScopes(*scopes)
	if err != nil {
		return err
	}
	if err := signIn(ctx); err != nil {
		return err
	}
	resp, err := e.cl.auth.CreateToken(ctx, connect.NewRequest(&v1.CreateTokenRequest{Name: positional[0], Scopes: list}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		t := resp.Msg.GetToken()
		fmt.Fprintf(w, "created %s %s holding %s, its secret shown this once\n%s\n", t.GetName(), t.GetId(), scopesText(t.GetScopes()), t.GetSecret())
	})
}

func runTokensRemove(ctx context.Context, e *env, args []string) error {
	fs := e.flags("tokens remove")
	signIn := accountFlags(e, fs)
	positional, err := e.parse(fs, args, 1, 1, "tokens remove <id> "+tokenSignIn)
	if err != nil {
		return err
	}
	if err := signIn(ctx); err != nil {
		return err
	}
	if _, err := e.cl.auth.DeleteToken(ctx, connect.NewRequest(&v1.DeleteTokenRequest{Id: positional[0]})); err != nil {
		return err
	}
	e.text("revoked %s\n", positional[0])
	return nil
}

func credentialCommands() command {
	return command{name: "credentials", summary: "credentials kept for providers serving models", run: runCredentialsList, local: true, sub: []command{
		{name: "list", summary: "list the credentials kept, never their secrets", run: runCredentialsList, local: true},
		{name: "add", summary: "keep a key for a provider source", run: runCredentialsAdd, local: true},
		{name: "update", summary: "rename, reorder, or replace the key of a credential", run: runCredentialsUpdate, local: true},
		{name: "signin", summary: "sign in with a provider and keep its tokens", run: runCredentialsSignIn, local: true},
		{name: "budgets", summary: "show or set a credential's budgets with its windows", run: runCredentialsBudgets, local: true},
		{name: "usage", summary: "requests and tokens each credential was used for, by model", run: runCredentialsUsage, local: true},
		{name: "remove", summary: "remove a credential", run: runCredentialsRemove, local: true},
	}}
}

func runCredentialsList(ctx context.Context, e *env, args []string) error {
	fs := e.flags("credentials list")
	signIn := accountFlags(e, fs)
	if _, err := e.parse(fs, args, 0, 0, "credentials list "+tokenSignIn); err != nil {
		return err
	}
	if err := signIn(ctx); err != nil {
		return err
	}
	resp, err := e.cl.auth.ListCredentials(ctx, connect.NewRequest(&v1.ListCredentialsRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, c := range resp.Msg.GetCredentials() {
			var spent []string
			for _, win := range c.GetWindows() {
				if win.GetSpent() {
					spent = append(spent, win.GetName())
				}
			}
			rows = append(rows, []string{c.GetId(), c.GetSourceId(), c.GetName(), enumShort(c.GetKind()), fmt.Sprint(c.GetPosition()), fmt.Sprint(len(c.GetBudgets())), orDash(strings.Join(spent, ", ")), when(c.GetExpiresAt(), "2006-01-02 15:04"), when(c.GetUpdatedAt(), "2006-01-02 15:04")})
		}
		table(w, []string{"ID", "SOURCE", "NAME", "KIND", "POSITION", "BUDGETS", "SPENT", "EXPIRES", "UPDATED"}, rows)
	})
}

func runCredentialsBudgets(ctx context.Context, e *env, args []string) error {
	fs := e.flags("credentials budgets")
	var set, drop multi
	fs.Var(&set, "budget", "a budget as name=N,unit=U,limit=L,period=P and any of seconds, start, zone, window, reserve, cached, model, repeatable, replacing one of the same name; a rolling period spans 300 to 8640000 seconds, and unit percent-used counts the percent of window used in place of a period")
	fs.Var(&drop, "drop", "the name of a budget to remove, repeatable")
	signIn := accountFlags(e, fs)
	positional, err := e.parse(fs, args, 1, 1, "credentials budgets <id> [--budget SPEC]... [--drop NAME]... "+tokenSignIn)
	if err != nil {
		return err
	}
	if err := signIn(ctx); err != nil {
		return err
	}
	list, err := e.cl.auth.ListCredentials(ctx, connect.NewRequest(&v1.ListCredentialsRequest{}))
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list.Msg.GetCredentials(), func(c *v1.ProviderCredential) bool { return c.GetId() == positional[0] })
	if i < 0 {
		return fmt.Errorf("no credential %s is kept, see nebu credentials list", positional[0])
	}
	cred := list.Msg.GetCredentials()[i]
	if len(set)+len(drop) > 0 {
		budgets := slices.DeleteFunc(slices.Clone(cred.GetBudgets()), func(b *v1.CredentialBudget) bool { return slices.Contains(drop, b.GetName()) })
		for _, s := range set {
			b, err := parseBudget(s)
			if err != nil {
				return err
			}
			if j := slices.IndexFunc(budgets, func(o *v1.CredentialBudget) bool { return o.GetName() == b.GetName() }); j >= 0 {
				budgets[j] = b
			} else {
				budgets = append(budgets, b)
			}
		}
		if _, err := e.cl.auth.PutCredential(ctx, connect.NewRequest(&v1.PutCredentialRequest{Id: cred.GetId(), Budgets: budgets, BudgetsSet: true})); err != nil {
			return err
		}
		again, err := e.cl.auth.ListCredentials(ctx, connect.NewRequest(&v1.ListCredentialsRequest{}))
		if err != nil {
			return err
		}
		for _, c := range again.Msg.GetCredentials() {
			if c.GetId() == cred.GetId() {
				cred = c
			}
		}
	}
	return e.print(cred, func(w io.Writer) {
		var rows [][]string
		for _, b := range cred.GetBudgets() {
			rows = append(rows, []string{b.GetName(), orDash(strings.Join(b.GetModels(), ", ")), enumShort(b.GetUnit()), strconv.FormatFloat(b.GetLimit(), 'f', -1, 64), periodText(b), fmt.Sprintf("%d%%", b.GetReservePercent())})
		}
		table(w, []string{"BUDGET", "MODELS", "UNIT", "LIMIT", "PERIOD", "RESERVE"}, rows)
		section(w, "windows")
		rows = nil
		for _, win := range cred.GetWindows() {
			state := "-"
			if win.GetSpent() {
				state = "spent"
			}
			rows = append(rows, []string{win.GetName(), enumShort(win.GetKind()), strconv.FormatFloat(win.GetUsed(), 'f', -1, 64), strconv.FormatFloat(win.GetLimit(), 'f', -1, 64), state, when(win.GetResetsAt(), "2006-01-02 15:04:05")})
		}
		table(w, []string{"WINDOW", "KIND", "USED", "LIMIT", "STATE", "RESETS"}, rows)
	})
}

// The span a budget counts over in words
func periodText(b *v1.CredentialBudget) string {
	switch b.GetPeriod() {
	case v1.BudgetPeriod_BUDGET_PERIOD_ROLLING:
		return fmt.Sprintf("rolling %s", time.Duration(b.GetSeconds())*time.Second)
	case v1.BudgetPeriod_BUDGET_PERIOD_REPORTED:
		return "follows " + b.GetWindow()
	case v1.BudgetPeriod_BUDGET_PERIOD_UNSPECIFIED:
		if b.GetWindow() != "" {
			return "follows " + b.GetWindow()
		}
		return "-"
	}
	out := fmt.Sprintf("%s from %d", enumShort(b.GetPeriod()), b.GetStart())
	if b.GetZone() != "" {
		out += " " + b.GetZone()
	}
	return out
}

// Reads a budget written as comma separated name=value pairs, model repeatable
func parseBudget(s string) (*v1.CredentialBudget, error) {
	b := &v1.CredentialBudget{}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok {
			return nil, fmt.Errorf("budget %q: %q is not name=value", s, pair)
		}
		var err error
		var n protoreflect.EnumNumber
		var u uint64
		switch k {
		case "name":
			b.Name = v
		case "model":
			b.Models = append(b.Models, v)
		case "unit":
			n, err = enumNamed(v1.BudgetUnit(0).Descriptor(), v, "unit")
			b.Unit = v1.BudgetUnit(n)
		case "period":
			n, err = enumNamed(v1.BudgetPeriod(0).Descriptor(), v, "period")
			b.Period = v1.BudgetPeriod(n)
		case "limit":
			b.Limit, err = strconv.ParseFloat(v, 64)
		case "seconds", "start", "reserve":
			u, err = strconv.ParseUint(v, 10, 32)
			switch k {
			case "seconds":
				b.Seconds = uint32(u)
			case "start":
				b.Start = uint32(u)
			default:
				b.ReservePercent = uint32(u)
			}
		case "zone":
			b.Zone = v
		case "window":
			b.Window = v
		case "cached":
			b.Cached, err = strconv.ParseBool(v)
		default:
			return nil, fmt.Errorf("budget %q: %s is not name, model, unit, limit, period, seconds, start, zone, window, reserve, or cached", s, k)
		}
		if err != nil {
			return nil, fmt.Errorf("budget %q: %s: %w", s, k, err)
		}
	}
	return b, nil
}

func runCredentialsUsage(ctx context.Context, e *env, args []string) error {
	fs := e.flags("credentials usage")
	since := fs.Duration("since", 30*24*time.Hour, "how far back to count")
	signIn := accountFlags(e, fs)
	positional, err := e.parse(fs, args, 0, 1, "credentials usage [<id>] [--since D] "+tokenSignIn)
	if err != nil {
		return err
	}
	if err := signIn(ctx); err != nil {
		return err
	}
	req := &v1.ListCredentialUsageRequest{From: timestamppb.New(time.Now().Add(-*since))}
	if len(positional) == 1 {
		req.CredentialId = positional[0]
	}
	resp, err := e.cl.auth.ListCredentialUsage(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, u := range resp.Msg.GetUsage() {
			rows = append(rows, []string{u.GetCredentialId(), u.GetModel(), strconv.FormatUint(u.GetRequests(), 10), strconv.FormatUint(u.GetInputTokens(), 10), strconv.FormatUint(u.GetOutputTokens(), 10), strconv.FormatUint(u.GetCachedTokens(), 10)})
		}
		table(w, []string{"CREDENTIAL", "MODEL", "REQUESTS", "INPUT", "OUTPUT", "CACHED"}, rows)
	})
}

// Defines the flags giving a key and returns a function reading it, prompting when neither flag gives one and prompt is set
func keyFlags(e *env, fs *flag.FlagSet) func(prompt bool) (string, error) {
	key := fs.String("key", "", "the key, prompted for when unset")
	stdin := fs.Bool("key-stdin", false, "read the key from standard input")
	return func(prompt bool) (string, error) {
		if *key != "" || *stdin || prompt {
			return e.secret(*key, *stdin, "Key: ")
		}
		return "", nil
	}
}

func runCredentialsAdd(ctx context.Context, e *env, args []string) error {
	fs := e.flags("credentials add")
	name := fs.String("name", "", "label, the source's name when empty")
	position := fs.Uint("position", 0, "order among the source's credentials from 1, last when unset")
	readKey := keyFlags(e, fs)
	signIn := accountFlags(e, fs)
	positional, err := e.parse(fs, args, 1, 1, "credentials add <source> [--name N] [--position N] [--key K | --key-stdin] "+tokenSignIn)
	if err != nil {
		return err
	}
	key, err := readKey(true)
	if err != nil {
		return err
	}
	if err := signIn(ctx); err != nil {
		return err
	}
	resp, err := e.cl.auth.PutCredential(ctx, connect.NewRequest(&v1.PutCredentialRequest{SourceId: positional[0], Name: *name, Secret: key, Position: uint32(*position)}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		c := resp.Msg.GetCredential()
		fmt.Fprintf(w, "kept %s for %s as %s, position %d\n", c.GetName(), c.GetSourceId(), c.GetId(), c.GetPosition())
	})
}

func runCredentialsUpdate(ctx context.Context, e *env, args []string) error {
	fs := e.flags("credentials update")
	name := fs.String("name", "", "new label, unchanged when empty")
	position := fs.Uint("position", 0, "new order among the source's credentials from 1, unchanged when unset")
	readKey := keyFlags(e, fs)
	signIn := accountFlags(e, fs)
	positional, err := e.parse(fs, args, 1, 1, "credentials update <id> [--name N] [--position N] [--key K | --key-stdin] "+tokenSignIn)
	if err != nil {
		return err
	}
	key, err := readKey(false)
	if err != nil {
		return err
	}
	if err := signIn(ctx); err != nil {
		return err
	}
	resp, err := e.cl.auth.PutCredential(ctx, connect.NewRequest(&v1.PutCredentialRequest{Id: positional[0], Name: *name, Secret: key, Position: uint32(*position)}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		c := resp.Msg.GetCredential()
		fmt.Fprintf(w, "updated %s %s for %s, position %d\n", c.GetName(), c.GetId(), c.GetSourceId(), c.GetPosition())
	})
}

func runCredentialsSignIn(ctx context.Context, e *env, args []string) error {
	fs := e.flags("credentials signin")
	name := fs.String("name", "", "label, the source's name when empty")
	again := fs.String("again", "", "the id of a sign-in with the source to authorize again in place with the same account, a new credential when empty")
	signIn := accountFlags(e, fs)
	positional, err := e.parse(fs, args, 1, 1, "credentials signin <source> [--name N] [--again ID] "+tokenSignIn)
	if err != nil {
		return err
	}
	if err := signIn(ctx); err != nil {
		return err
	}
	started, err := e.cl.auth.StartSignIn(ctx, connect.NewRequest(&v1.StartSignInRequest{SourceId: positional[0], Name: *name, CredentialId: *again}))
	if err != nil {
		return err
	}
	in := started.Msg.GetSignIn()
	var callback string
	if in.GetCode() != "" {
		fmt.Fprintf(e.errw, "open %s and enter %s, before %s\n", in.GetUrl(), in.GetCode(), when(in.GetExpiresAt(), "15:04"))
	} else {
		fmt.Fprintf(e.errw, "open %s and approve, before %s\nthen paste the address the browser lands on, or the code the page shows: ", in.GetUrl(), when(in.GetExpiresAt(), "15:04"))
		line, err := bufio.NewReader(e.in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		callback = strings.TrimSpace(line)
	}
	resp, err := e.cl.auth.FinishSignIn(ctx, connect.NewRequest(&v1.FinishSignInRequest{Id: in.GetId(), Callback: callback}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		c := resp.Msg.GetCredential()
		fmt.Fprintf(w, "signed in, kept %s for %s as %s\n", c.GetName(), c.GetSourceId(), c.GetId())
	})
}

func runCredentialsRemove(ctx context.Context, e *env, args []string) error {
	fs := e.flags("credentials remove")
	signIn := accountFlags(e, fs)
	positional, err := e.parse(fs, args, 1, 1, "credentials remove <id> "+tokenSignIn)
	if err != nil {
		return err
	}
	if err := signIn(ctx); err != nil {
		return err
	}
	resp, err := e.cl.auth.DeleteCredential(ctx, connect.NewRequest(&v1.DeleteCredentialRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	if resp.Msg.GetRevocationUnconfirmed() {
		fmt.Fprintln(e.errw, "the provider has yet to confirm ending the sign-in's session, the daemon keeps asking; disconnect nebu at the provider to end it now")
	}
	e.text("removed %s\n", positional[0])
	return nil
}

// Takes a password from the flag, standard input, or a terminal prompt without echo
func (e *env) password(flagValue string, fromStdin, confirm bool) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if fromStdin {
		line, err := bufio.NewReader(e.in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	f, ok := e.in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", errors.New("no terminal to prompt on, pass --password or --password-stdin")
	}
	first, err := e.prompt(f, "Password: ")
	if err != nil {
		return "", err
	}
	if confirm {
		second, err := e.prompt(f, "Confirm password: ")
		if err != nil {
			return "", err
		}
		if first != second {
			return "", errors.New("passwords do not match")
		}
	}
	return first, nil
}

// Takes a secret from the flag, standard input, or a terminal prompt without echo
func (e *env) secret(flagValue string, fromStdin bool, label string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if fromStdin {
		line, err := bufio.NewReader(e.in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
	f, ok := e.in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", errors.New("no terminal to prompt on, pass --key or --key-stdin")
	}
	return e.prompt(f, label)
}

func (e *env) prompt(f *os.File, label string) (string, error) {
	fmt.Fprint(e.errw, label)
	raw, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(e.errw)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
