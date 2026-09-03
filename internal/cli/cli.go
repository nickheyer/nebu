// Package cli implements the nebu command line.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nickheyer/nebu/internal/daemon"
	"github.com/nickheyer/nebu/pkg/config"
	"github.com/nickheyer/nebu/pkg/logger"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

type command struct {
	name    string
	summary string
	run     func(ctx context.Context, e *env, args []string) error
	sub     []command
}

func commands() []command {
	return []command{
		{name: "serve", summary: "run the daemon", run: runServe},
		{name: "doctor", summary: "probe the host and check every dependency", run: runDoctor},
		{name: "host", summary: "show the probed host profile", run: runHost},
		{name: "sources", summary: "list configured sources", run: runSources},
		{name: "search", summary: "search a source catalog", run: runSearch},
		{name: "inspect", summary: "estimate memory fit for every weight group of a model", run: runInspect},
		{name: "pull", summary: "download a weight group into the store", run: runPull},
		{name: "list", summary: "list stored models", run: runList},
		{name: "remove", summary: "remove a stored model", run: runRemove},
		{name: "store", summary: "store status, gc, and verify", run: runStoreStatus, sub: []command{
			{name: "status", summary: "show store counters", run: runStoreStatus},
			{name: "gc", summary: "remove unreferenced blobs", run: runStoreGc},
			{name: "verify", summary: "rehash stored blobs", run: runStoreVerify},
		}},
		{name: "tasks", summary: "list, watch, and cancel tasks", run: runTasksList, sub: []command{
			{name: "list", summary: "list tasks", run: runTasksList},
			{name: "watch", summary: "follow one task", run: runTasksWatch},
			{name: "cancel", summary: "cancel one task", run: runTasksCancel},
		}},
		{name: "runtimes", summary: "list runtimes and host compatibility", run: runRuntimes},
		{name: "version", summary: "print version", run: runVersion},
	}
}

// Walks nested command tables and returns the command and its args
func resolve(cmds []command, args []string) (*command, []string) {
	if len(args) == 0 {
		return nil, args
	}
	for i := range cmds {
		if cmds[i].name != args[0] {
			continue
		}
		if sub, rest := resolve(cmds[i].sub, args[1:]); sub != nil {
			return sub, rest
		}
		return &cmds[i], args[1:]
	}
	return nil, args
}

// Shared state for one invocation
type env struct {
	cfg     *v1.Config
	log     *slog.Logger
	out     io.Writer
	errw    io.Writer
	json    bool
	cl      *clients
	daemon  *daemon.Daemon
	closers []io.Closer
}

// Stops an in process daemon and its logger when one was started
func (e *env) close() {
	if e.daemon != nil {
		e.daemon.Close()
	}
	for _, c := range e.closers {
		c.Close()
	}
}

// Runs the CLI and returns an exit code
func Main(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fs := flag.NewFlagSet("nebu", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "config file path")
	addr := fs.String("addr", "", "daemon address, in process when empty")
	jsonOut := fs.Bool("json", false, "print responses as JSON")
	fs.Usage = func() { usage(stderr, fs) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		usage(stderr, fs)
		return 2
	}
	cmd, cmdArgs := resolve(commands(), rest)
	if cmd == nil {
		fmt.Fprintf(stderr, "nebu: unknown command %q\n", rest[0])
		usage(stderr, fs)
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "nebu:", err)
		return 1
	}
	if *addr != "" {
		cfg.Addr = *addr
	}
	log, closer, err := logger.New(cfg.GetLogging())
	if err != nil {
		fmt.Fprintln(stderr, "nebu:", err)
		return 1
	}
	defer closer.Close()
	e := &env{cfg: cfg, log: log, out: stdout, errw: stderr, json: *jsonOut}
	defer e.close()
	if err := cmd.run(ctx, e, cmdArgs); err != nil {
		fmt.Fprintln(stderr, "nebu:", err)
		return 1
	}
	return 0
}

func usage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(w, "usage: nebu [flags] <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
		for _, s := range c.sub {
			fmt.Fprintf(w, "    %-8s %s\n", s.name, s.summary)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "flags:")
	fs.PrintDefaults()
}

func (e *env) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.errw)
	return fs
}

// Parses flags anywhere among positionals
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// Repeated string flag
type multi []string

func (m *multi) String() string { return fmt.Sprint([]string(*m)) }

func (m *multi) Set(v string) error {
	*m = append(*m, v)
	return nil
}
