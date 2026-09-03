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

	"github.com/nickheyer/nebu/pkg/config"
	"github.com/nickheyer/nebu/pkg/logger"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

type command struct {
	name    string
	summary string
	run     func(ctx context.Context, e *env, args []string) error
}

func commands() []command {
	return []command{
		{"serve", "run the daemon", runServe},
		{"doctor", "probe the host and check every dependency", runDoctor},
		{"host", "show the probed host profile", runHost},
		{"sources", "list configured sources", runSources},
		{"search", "search a source catalog", runSearch},
		{"inspect", "estimate memory fit for every weight group of a model", runInspect},
		{"runtimes", "list runtimes and host compatibility", runRuntimes},
		{"version", "print version", runVersion},
	}
}

// Shared state for one invocation
type env struct {
	cfg  *v1.Config
	log  *slog.Logger
	out  io.Writer
	errw io.Writer
	json bool
	cl   *clients
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
	var cmd *command
	for _, c := range commands() {
		if c.name == rest[0] {
			cmd = &c
			break
		}
	}
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
	if err := cmd.run(ctx, e, rest[1:]); err != nil {
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
