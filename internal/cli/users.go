package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/auth"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/term"
)

func userCommands() command {
	return command{name: "users", summary: "local accounts for the web UI", run: runUsersList, local: true, sub: []command{
		{name: "list", summary: "list accounts", run: runUsersList, local: true},
		{name: "add", summary: "add an account", run: runUsersAdd, local: true},
		{name: "remove", summary: "remove an account", run: runUsersRemove, local: true},
		{name: "passwd", summary: "change an account's password", run: runUsersPasswd, local: true},
	}}
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

func (e *env) prompt(f *os.File, label string) (string, error) {
	fmt.Fprint(e.errw, label)
	raw, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(e.errw)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
