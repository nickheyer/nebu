// Package cli implements the nebu command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/nickheyer/nebu/internal/daemon"
	"github.com/nickheyer/nebu/pkg/config"
	"github.com/nickheyer/nebu/pkg/logger"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type command struct {
	name    string
	summary string
	run     func(ctx context.Context, e *env, args []string) error
	sub     []command
	// Answers from a daemon built in process when none is listening
	local bool
}

func commands() []command {
	return []command{
		{name: "serve", summary: "run the daemon", run: runServe, local: true},
		{name: "doctor", summary: "probe the host and check every dependency as a task", run: runDoctor, local: true},
		{name: "host", summary: "show the probed host profile", run: runHost, local: true},
		{name: "sources", summary: "configured sources", run: runSources, local: true, sub: []command{
			{name: "list", summary: "list sources with their sorts, facets, and auth state", run: runSources, local: true},
			{name: "providers", summary: "list providers with the settings their sources accept", run: runSourcesProviders, local: true},
			{name: "add", summary: "add a source", run: runSourcesAdd},
			{name: "update", summary: "change the settings of a source", run: runSourcesUpdate},
			{name: "remove", summary: "remove a source", run: runSourcesRemove},
		}},
		{name: "search", summary: "search or browse a source catalog", run: runSearch, local: true},
		{name: "revisions", summary: "list revisions, tags, or versions of a repository", run: runRevisions, local: true},
		{name: "card", summary: "print the model card a source publishes", run: runCard, local: true},
		{name: "inspect", summary: "estimate memory fit for every weight group of a model", run: runInspect, local: true},
		{name: "pull", summary: "download a weight group into the store", run: runPull, local: true},
		{name: "list", summary: "list stored models", run: runList, local: true},
		{name: "remove", summary: "remove a stored model", run: runRemove, local: true},
		{name: "store", summary: "store status, gc, and verify", run: runStoreStatus, local: true, sub: []command{
			{name: "status", summary: "show store counters", run: runStoreStatus, local: true},
			{name: "gc", summary: "remove unreferenced blobs", run: runStoreGc, local: true},
			{name: "verify", summary: "rehash stored blobs", run: runStoreVerify, local: true},
			{name: "export", summary: "copy stored models into a mirror directory", run: runStoreExport, local: true},
		}},
		{name: "tasks", summary: "list, watch, and cancel tasks", run: runTasksList, local: true, sub: []command{
			{name: "list", summary: "list tasks", run: runTasksList, local: true},
			{name: "watch", summary: "follow one task", run: runTasksWatch},
			{name: "cancel", summary: "cancel one task", run: runTasksCancel},
		}},
		{name: "runtimes", summary: "runtimes and their installs", run: runRuntimes, local: true, sub: []command{
			{name: "list", summary: "list runtimes and host compatibility", run: runRuntimes, local: true},
			{name: "show", summary: "show a runtime with every param it takes", run: runRuntimesShow, local: true},
			{name: "installs", summary: "list installs", run: runRuntimesInstalls, local: true},
			{name: "adopt", summary: "record a binary already on the host", run: runRuntimesAdopt, local: true},
			{name: "install", summary: "install a runtime by the method its manifest selects for this host", run: runRuntimesInstall, local: true},
			{name: "remove", summary: "remove an install", run: runRuntimesRemove, local: true},
			{name: "recipes", summary: "list build recipes and what this host selects", run: runRuntimesRecipes, local: true},
		}},
		{name: "build", summary: "build a runtime from its recipe", run: runBuild, local: true},
		{name: "builds", summary: "list, show, and remove builds", run: runBuildsList, local: true, sub: []command{
			{name: "list", summary: "list builds", run: runBuildsList, local: true},
			{name: "show", summary: "show one build", run: runBuildsShow, local: true},
			{name: "remove", summary: "remove a build and its install", run: runBuildsRemove, local: true},
		}},
		{name: "run", summary: "start a stored model on a runtime", run: runRun},
		{name: "swap", summary: "replace what a slot serves without dropping its name", run: runSwap},
		{name: "slots", summary: "reservations of devices and memory", run: runSlotsList, sub: []command{
			{name: "list", summary: "list slots", run: runSlotsList},
			{name: "create", summary: "create a slot", run: runSlotsCreate},
			{name: "show", summary: "show a slot and its occupant", run: runSlotsShow},
			{name: "update", summary: "change slot settings", run: runSlotsUpdate},
			{name: "evict", summary: "stop the occupant and forget its model, keep the slot", run: runSlotsEvict},
			{name: "relaunch", summary: "run the slot's model again after a failure", run: runSlotsRelaunch},
			{name: "remove", summary: "delete a slot", run: runSlotsRemove},
		}},
		{name: "routes", summary: "public names the gateway answers for", run: runRoutesList, sub: []command{
			{name: "list", summary: "list routes", run: runRoutesList},
			{name: "add", summary: "alias a name onto a running instance", run: runRoutesAdd},
			{name: "remove", summary: "remove an alias", run: runRoutesRemove},
		}},
		{name: "gateway", summary: "gateway listeners, routes, counters, and recent requests", run: runGateway, sub: []command{
			{name: "status", summary: "show listeners, routes, and counters", run: runGateway},
			{name: "traces", summary: "list recent requests through the gateway", run: runGatewayTraces},
			{name: "trace", summary: "show one request with its bodies", run: runGatewayTrace},
		}},
		{name: "chat", summary: "talk to a running model through the gateway", run: runChat},
		{name: "events", summary: "stream daemon events as JSON lines", run: runEvents},
		{name: "ps", summary: "list running instances", run: runPs},
		{name: "show", summary: "show instance info", run: runShow},
		{name: "stop", summary: "stop an instance", run: runStop},
		{name: "logs", summary: "show or follow instance output", run: runLogs},
		{name: "version", summary: "print version", run: runVersion, local: true},
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
	cmd  *command
	cfg  *v1.Config
	log  *slog.Logger
	out  io.Writer
	errw io.Writer
	in   io.Reader
	json bool
	// The daemon address once looked for, empty when this process stands in
	addr     string
	resolved bool
	cl       *clients
	daemon   *daemon.Daemon
	closers  []io.Closer
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
	daemon.Version = buildVersion()
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
	e := &env{cmd: cmd, cfg: cfg, log: log, out: stdout, errw: stderr, in: os.Stdin, json: *jsonOut}
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

// Parses flags anywhere among positionals, holds them to the usage line, and readies the clients
//
// A command that is not local needs a daemon listening, so that is checked before anything is dialed.
// Max below zero takes any number of positionals.
func (e *env) parse(fs *flag.FlagSet, args []string, min, max int, usage string) ([]string, error) {
	positional, err := splitFlags(fs, args)
	if err != nil {
		return nil, err
	}
	if len(positional) < min || max >= 0 && len(positional) > max {
		return nil, errors.New("usage: nebu " + usage)
	}
	if !e.cmd.local {
		if err := e.requireDaemon(); err != nil {
			return nil, err
		}
	}
	e.cl, err = e.clients()
	return positional, err
}

// Parses flags anywhere among positionals
func splitFlags(fs *flag.FlagSet, args []string) ([]string, error) {
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

// Reads name=value pairs into a map
func pairs(items []string, what string) (map[string]string, error) {
	out := map[string]string{}
	for _, item := range items {
		k, v, ok := strings.Cut(item, "=")
		if !ok {
			return nil, fmt.Errorf("%s %q: expected name=value", what, item)
		}
		out[k] = v
	}
	return out, nil
}

// Splits repo@revision
func splitRef(ref string) (string, string) {
	if i := strings.LastIndex(ref, "@"); i > 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// Prints JSON when requested, else the table renderer
func (e *env) print(msg proto.Message, render func(w io.Writer)) error {
	if e.json {
		data, err := protojson.MarshalOptions{Multiline: true, Indent: "  ", UseProtoNames: true}.Marshal(msg)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(e.out, string(data))
		return err
	}
	if render != nil {
		render(e.out)
	}
	return nil
}

// Prints the message unless an error came with it, what a call's last line does
func (e *env) done(msg proto.Message, err error) error {
	if err != nil {
		return err
	}
	return e.print(msg, nil)
}

// Prints a line of text unless JSON was asked for
func (e *env) text(format string, args ...any) {
	if !e.json {
		fmt.Fprintf(e.out, format, args...)
	}
}

const factsWidth = 72

func table(w io.Writer, headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	if len(headers) > 0 {
		fmt.Fprintln(tw, strings.Join(headers, "\t"))
	}
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

func section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n%s\n", strings.ToUpper(title))
}

// Rows of a map in key order
func rowsOf(m map[string]string) [][]string {
	var rows [][]string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		rows = append(rows, []string{k, m[k]})
	}
	return rows
}

// One line of key=value pairs in key order, cut to the facts width
func compact(m map[string]string) string {
	var parts []string
	for _, r := range rowsOf(m) {
		parts = append(parts, r[0]+"="+r[1])
	}
	return truncate(strings.Join(parts, " "), factsWidth)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "..."
}

// A timestamp in the layout, dash when unset
func when(ts *timestamppb.Timestamp, layout string) string {
	if ts == nil {
		return "-"
	}
	return ts.AsTime().Local().Format(layout)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return orDash(c)
}

func yes(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func humanCount(n uint64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.0fM", float64(n)/1e6)
	}
	return fmt.Sprint(n)
}

func runVersion(ctx context.Context, e *env, args []string) error {
	_, err := fmt.Fprintln(e.out, "nebu "+buildVersion())
	return err
}

// Runs the daemon in this process until interrupted
func runServe(ctx context.Context, e *env, args []string) error {
	if _, err := splitFlags(e.flags("serve"), args); err != nil {
		return err
	}
	d, err := daemon.New(e.cfg, e.log)
	if err != nil {
		return err
	}
	return d.ListenAndServe(ctx)
}

// Reads the module version and commit out of the binary
func buildVersion() string {
	version, revision := "devel", ""
	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				revision = s.Value[:7]
			}
		}
	}
	return strings.TrimSpace(version + " " + revision)
}
