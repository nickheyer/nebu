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
		{name: "sources", summary: "configured sources", run: runSources, sub: []command{
			{name: "list", summary: "list sources with their sorts, facets, and auth state", run: runSources},
			{name: "add", summary: "add a source", run: runSourcesAdd},
			{name: "update", summary: "change the settings of a source", run: runSourcesUpdate},
			{name: "remove", summary: "remove a source", run: runSourcesRemove},
		}},
		{name: "search", summary: "search or browse a source catalog", run: runSearch},
		{name: "revisions", summary: "list revisions, tags, or versions of a repository", run: runRevisions},
		{name: "card", summary: "print the model card a source publishes", run: runCard},
		{name: "inspect", summary: "estimate memory fit for every weight group of a model", run: runInspect},
		{name: "pull", summary: "download a weight group into the store", run: runPull},
		{name: "list", summary: "list stored models", run: runList},
		{name: "remove", summary: "remove a stored model", run: runRemove},
		{name: "store", summary: "store status, gc, and verify", run: runStoreStatus, sub: []command{
			{name: "status", summary: "show store counters", run: runStoreStatus},
			{name: "gc", summary: "remove unreferenced blobs", run: runStoreGc},
			{name: "verify", summary: "rehash stored blobs", run: runStoreVerify},
			{name: "export", summary: "copy stored models into a mirror directory", run: runStoreExport},
		}},
		{name: "tasks", summary: "list, watch, and cancel tasks", run: runTasksList, sub: []command{
			{name: "list", summary: "list tasks", run: runTasksList},
			{name: "watch", summary: "follow one task", run: runTasksWatch},
			{name: "cancel", summary: "cancel one task", run: runTasksCancel},
		}},
		{name: "runtimes", summary: "runtimes and their installs", run: runRuntimes, sub: []command{
			{name: "list", summary: "list runtimes and host compatibility", run: runRuntimes},
			{name: "installs", summary: "list installs", run: runRuntimesInstalls},
			{name: "adopt", summary: "record a binary already on the host", run: runRuntimesAdopt},
			{name: "install", summary: "download a prebuilt release for this host", run: runRuntimesInstall},
			{name: "remove", summary: "remove an install", run: runRuntimesRemove},
			{name: "recipes", summary: "list build recipes and what this host selects", run: runRuntimesRecipes},
		}},
		{name: "build", summary: "build a runtime from its recipe", run: runBuild},
		{name: "builds", summary: "list, show, and remove builds", run: runBuildsList, sub: []command{
			{name: "list", summary: "list builds", run: runBuildsList},
			{name: "show", summary: "show one build", run: runBuildsShow},
			{name: "remove", summary: "remove a build and its install", run: runBuildsRemove},
		}},
		{name: "run", summary: "start a stored model on a runtime", run: runRun},
		{name: "swap", summary: "replace what a slot serves without dropping its name", run: runSwap},
		{name: "slots", summary: "reservations of devices and memory", run: runSlotsList, sub: []command{
			{name: "list", summary: "list slots", run: runSlotsList},
			{name: "create", summary: "create a slot", run: runSlotsCreate},
			{name: "show", summary: "show a slot and its occupant", run: runSlotsShow},
			{name: "update", summary: "change slot settings", run: runSlotsUpdate},
			{name: "evict", summary: "stop the occupant, keep the slot", run: runSlotsEvict},
			{name: "remove", summary: "delete a slot", run: runSlotsRemove},
		}},
		{name: "routes", summary: "public names the gateway answers for", run: runRoutesList, sub: []command{
			{name: "list", summary: "list routes", run: runRoutesList},
			{name: "add", summary: "alias a name onto a running instance", run: runRoutesAdd},
			{name: "remove", summary: "remove an alias", run: runRoutesRemove},
		}},
		{name: "gateway", summary: "gateway listeners, routes, and counters", run: runGateway},
		{name: "monitor", summary: "watch sources for new revisions and quants", run: runMonitorList, sub: []command{
			{name: "list", summary: "list watches", run: runMonitorList},
			{name: "add", summary: "watch a repository", run: runMonitorAdd},
			{name: "remove", summary: "stop watching", run: runMonitorRemove},
			{name: "check", summary: "check now", run: runMonitorCheck},
			{name: "findings", summary: "list findings", run: runMonitorFindings},
			{name: "ack", summary: "acknowledge a finding", run: runMonitorAck},
		}},
		{name: "events", summary: "stream daemon events as JSON lines", run: runEvents},
		{name: "ps", summary: "list running instances", run: runPs},
		{name: "show", summary: "show instance info", run: runShow},
		{name: "stop", summary: "stop an instance", run: runStop},
		{name: "logs", summary: "show or follow instance output", run: runLogs},
		{name: "version", summary: "print version", run: runVersion},
	}
}

// Walks nested command tables to the command and its args
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
	remote  bool
	cl      *clients
	daemon  *daemon.Daemon
	closers []io.Closer
}

// Stops the in process daemon and logger when present
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
