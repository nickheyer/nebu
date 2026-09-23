package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"connectrpc.com/connect"
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
			rows = append(rows, []string{u.GetUsername(), u.GetCreatedAt().AsTime().Local().Format("2006-01-02 15:04"), u.GetUpdatedAt().AsTime().Local().Format("2006-01-02 15:04")})
		}
		table(w, []string{"USERNAME", "CREATED", "PASSWORD CHANGED"}, rows)
	})
}

func runUsersAdd(ctx context.Context, e *env, args []string) error {
	fs := e.flags("users add")
	password := fs.String("password", "", "the password, prompted for when unset")
	stdin := fs.Bool("password-stdin", false, "read the password from standard input")
	positional, err := e.parse(fs, args, 1, 1, "users add <username> [--password P | --password-stdin]")
	if err != nil {
		return err
	}
	pw, err := e.password(*password, *stdin, true)
	if err != nil {
		return err
	}
	resp, err := e.cl.auth.CreateUser(ctx, connect.NewRequest(&v1.CreateUserRequest{Username: positional[0], Password: pw}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "added %s\n", resp.Msg.GetUser().GetUsername()) })
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
