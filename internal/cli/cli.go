// Package cli implements the nebu command line
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/nickheyer/nebu/internal/daemon"
	"github.com/nickheyer/nebu/pkg/config"
	"github.com/nickheyer/nebu/pkg/launch"
	"github.com/nickheyer/nebu/pkg/logger"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"sigs.k8s.io/yaml"
)

type command struct {
	name    string
	summary string
	run     func(ctx context.Context, e *env, args []string) error
	sub     []command
	// Can use an in-process daemon when no daemon is listening
	local bool
}

func commands() []command {
	return []command{
		{name: "serve", summary: "run the daemon", run: runServe, local: true},
		{name: "doctor", summary: "check host dependencies", run: runDoctor, local: true},
		{name: "host", summary: "show the probed host profile", run: runHost, local: true},
		{name: "sources", summary: "configured sources", run: runSources, local: true, sub: []command{
			{name: "list", summary: "list sources with their sorts, facets, and auth state", run: runSources, local: true},
			{name: "providers", summary: "list providers and source settings", run: runSourcesProviders, local: true},
			{name: "add", summary: "add a source", run: runSourcesAdd},
			{name: "update", summary: "change source settings", run: runSourcesUpdate},
			{name: "remove", summary: "remove a source", run: runSourcesRemove},
		}},
		{name: "search", summary: "search or browse a source catalog", run: runSearch, local: true},
		{name: "revisions", summary: "list revisions, tags, or versions of a repository", run: runRevisions, local: true},
		{name: "card", summary: "print a model card", run: runCard, local: true},
		{name: "inspect", summary: "estimate memory use for each weight group", run: runInspect, local: true},
		{name: "pull", summary: "download a weight group with its blueprint parts, or files of a repository", run: runPull, local: true},
		{name: "parts", summary: "list a weight group's parts", run: runParts, local: true},
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
			{name: "show", summary: "show a runtime and its params", run: runRuntimesShow, local: true},
			{name: "installs", summary: "list installs", run: runRuntimesInstalls, local: true},
			{name: "adopt", summary: "record a binary already on the host", run: runRuntimesAdopt, local: true},
			{name: "install", summary: "install a runtime", run: runRuntimesInstall, local: true},
			{name: "remove", summary: "remove an install", run: runRuntimesRemove, local: true},
			{name: "recipes", summary: "list build recipes and host defaults", run: runRuntimesRecipes, local: true},
		}},
		{name: "build", summary: "build a runtime from its recipe", run: runBuild, local: true},
		{name: "builds", summary: "list, show, and remove builds", run: runBuildsList, local: true, sub: []command{
			{name: "list", summary: "list builds", run: runBuildsList, local: true},
			{name: "show", summary: "show one build", run: runBuildsShow, local: true},
			{name: "remove", summary: "remove a build and its install", run: runBuildsRemove, local: true},
		}},
		{name: "run", summary: "start a stored model on a runtime", run: runRun},
		{name: "swap", summary: "replace a slot's model", run: runSwap},
		{name: "slots", summary: "device and memory reservations", run: runSlotsList, sub: []command{
			{name: "list", summary: "list slots", run: runSlotsList},
			{name: "create", summary: "create a slot", run: runSlotsCreate},
			{name: "show", summary: "show a slot and its occupant", run: runSlotsShow},
			{name: "update", summary: "change slot settings", run: runSlotsUpdate},
			{name: "evict", summary: "stop and clear a slot's model", run: runSlotsEvict},
			{name: "relaunch", summary: "run the slot's model again after a failure", run: runSlotsRelaunch},
			{name: "remove", summary: "delete a slot", run: runSlotsRemove},
		}},
		poolCommands(),
		routeCommands(),
		objectiveCommands(),
		schedulerCommands(),
		{name: "gateway", summary: "gateway listeners, pools, routes, counters, and recent requests", run: runGateway, sub: []command{
			{name: "status", summary: "show listeners, pools, routes, counters, and the metrics address", run: runGateway},
			{name: "traces", summary: "list recent requests through the gateway", run: runGatewayTraces},
			{name: "trace", summary: "show one request with its bodies", run: runGatewayTrace},
		}},
		{name: "chat", summary: "talk to a running model through the gateway", run: runChat},
		meshCommands(),
		formationCommands(),
		botCommands(),
		userCommands(),
		tokenCommands(),
		{name: "events", summary: "stream daemon events as JSON lines", run: runEvents},
		{name: "ps", summary: "list running instances", run: runPs},
		{name: "adapters", summary: "adapters a running instance applies", run: runAdaptersList, sub: []command{
			{name: "list", summary: "list the adapters an instance applies", run: runAdaptersList},
			{name: "load", summary: "load an adapter into a running instance", run: runAdaptersLoad},
			{name: "unload", summary: "unload an adapter from a running instance", run: runAdaptersUnload},
		}},
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
	cmd *command
	cfg *v1.Config
	log *slog.Logger
	// The log level handle a config reload sets and the config file the daemon reloads
	level      *slog.LevelVar
	configPath string
	// Recent log lines streamed to the Host page
	recent *launch.Log
	out    io.Writer
	errw   io.Writer
	in     io.Reader
	json   bool
	// Resolved daemon address, or empty for an in-process daemon
	addr     string
	resolved bool
	cl       *clients
	daemon   *daemon.Daemon
	closers  []io.Closer
}

// The configured token, else the one the daemon on this host wrote
func (e *env) token() string {
	if t := e.cfg.GetAuth().GetToken(); t != "" || e.cfg.GetAuth().GetDisabled() {
		return t
	}
	data, err := os.ReadFile(filepath.Join(e.cfg.GetDataDir(), config.TokenFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
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
	log, recent, level, closer, err := logger.New(cfg.GetLogging())
	if err != nil {
		fmt.Fprintln(stderr, "nebu:", err)
		return 1
	}
	defer closer.Close()
	e := &env{cmd: cmd, cfg: cfg, log: log, level: level, configPath: *configPath, recent: recent, out: stdout, errw: stderr, in: os.Stdin, json: *jsonOut}
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

// Parses flags and positional args, validates usage, and creates clients
// Non-local commands require a running daemon. Negative max allows any arg count
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

// Unsigned 32 bit flag value
type u32 uint32

func (v *u32) String() string { return strconv.FormatUint(uint64(*v), 10) }

func (v *u32) Set(s string) error {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return fmt.Errorf("%q is not a whole number from 0 to %d", s, uint32(math.MaxUint32))
	}
	*v = u32(n)
	return nil
}

// Signed 32 bit flag value
type i32 int32

func (v *i32) String() string { return strconv.FormatInt(int64(*v), 10) }

func (v *i32) Set(s string) error {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return fmt.Errorf("%q is not a whole number from %d to %d", s, math.MinInt32, math.MaxInt32)
	}
	*v = i32(n)
	return nil
}

// Names of the flags given on the command line
func given(fs *flag.FlagSet) map[string]bool {
	out := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { out[f.Name] = true })
	return out
}

// A duration such as 90s as a count of whole units, zero when empty, refusing negatives, fractions, and overflow
func wholeUnits(s string, unit time.Duration) (uint32, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	switch {
	case d < 0:
		return 0, fmt.Errorf("%s is negative", s)
	case d%unit != 0:
		return 0, fmt.Errorf("%s is not a whole number of %s", s, unitName(unit))
	case d/unit > math.MaxUint32:
		return 0, fmt.Errorf("%s is longer than %d %s", s, uint32(math.MaxUint32), unitName(unit))
	}
	return uint32(d / unit), nil
}

// Plural name of a duration unit for errors
func unitName(unit time.Duration) string {
	if unit == time.Second {
		return "seconds"
	}
	return "milliseconds"
}

// A duration such as 30m in whole seconds, zero when empty
func parseSeconds(s string) (uint32, error) { return wholeUnits(s, time.Second) }

// A duration such as 1500ms in whole milliseconds, zero when empty
func parseMillis(s string) (uint32, error) { return wholeUnits(s, time.Millisecond) }

// An enum value by its short name such as fail-open or its full name, refusing the unspecified value
func enumNamed(ed protoreflect.EnumDescriptor, name, what string) (protoreflect.EnumNumber, error) {
	prefix := strings.TrimSuffix(string(ed.Values().ByNumber(0).Name()), "UNSPECIFIED")
	want := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
	var names []string
	values := ed.Values()
	for i := range values.Len() {
		v := values.Get(i)
		if v.Number() == 0 {
			continue
		}
		full := string(v.Name())
		if want != "" && (full == want || full == prefix+want) {
			return v.Number(), nil
		}
		names = append(names, strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(full, prefix)), "_", "-"))
	}
	return 0, fmt.Errorf("%s %q is not %s", what, name, orList(names))
}

// Joins names as a, b, or c
func orList(names []string) string {
	switch len(names) {
	case 0:
		return "anything"
	case 1:
		return names[0]
	case 2:
		return names[0] + " or " + names[1]
	}
	return strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
}

// Lowercase short name of an enum value with dashes, a dash for the unspecified value
func enumShort(e protoreflect.Enum) string {
	if e.Number() == 0 {
		return "-"
	}
	return strings.ReplaceAll(text.Enum(e), "_", "-")
}

// Reads a YAML or JSON file into the message, enum values taking short names such as fail-open
func readMessage(path string, msg proto.Message) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	js, err := yaml.YAMLToJSON(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(js))
	dec.UseNumber()
	var tree map[string]any
	if err := dec.Decode(&tree); err != nil {
		return fmt.Errorf("%s: the file must hold a mapping of fields to values", path)
	}
	if err := expandEnums(msg.ProtoReflect().Descriptor(), tree); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if js, err = json.Marshal(tree); err != nil {
		return err
	}
	if err := protojson.Unmarshal(js, msg); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Rewrites short enum names in a decoded message tree to the full names protojson reads
func expandEnums(md protoreflect.MessageDescriptor, tree map[string]any) error {
	for key, value := range tree {
		fd := md.Fields().ByName(protoreflect.Name(key))
		if fd == nil {
			fd = md.Fields().ByJSONName(key)
		}
		if fd == nil {
			continue
		}
		var err error
		switch {
		case fd.IsMap():
			if m, ok := value.(map[string]any); ok {
				for k, item := range m {
					if m[k], err = expandEnum(fd.MapValue(), item); err != nil {
						return err
					}
				}
			}
		case fd.IsList():
			if items, ok := value.([]any); ok {
				for i, item := range items {
					if items[i], err = expandEnum(fd, item); err != nil {
						return err
					}
				}
			}
		default:
			if tree[key], err = expandEnum(fd, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// One field value with a short enum name made full and nested messages expanded
func expandEnum(fd protoreflect.FieldDescriptor, value any) (any, error) {
	switch fd.Kind() {
	case protoreflect.EnumKind:
		name, ok := value.(string)
		if !ok || fd.Enum().Values().ByName(protoreflect.Name(name)) != nil {
			return value, nil
		}
		n, err := enumNamed(fd.Enum(), name, string(fd.Name()))
		if err != nil {
			return nil, err
		}
		return string(fd.Enum().Values().ByNumber(n).Name()), nil
	case protoreflect.MessageKind:
		if m, ok := value.(map[string]any); ok {
			return value, expandEnums(fd.Message(), m)
		}
	}
	return value, nil
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

// Prints a successful response or returns its error
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

// Sorted key=value pairs separated by spaces
func joinPairs(m map[string]string) string {
	var parts []string
	for _, r := range rowsOf(m) {
		parts = append(parts, r[0]+"="+r[1])
	}
	return strings.Join(parts, " ")
}

// Sorted key=value pairs, truncated to the facts column width
func compact(m map[string]string) string {
	return truncate(joinPairs(m), factsWidth)
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
	d, err := daemon.New(e.cfg, e.log, e.level, e.recent)
	if err != nil {
		return err
	}
	d.ConfigPath = e.configPath
	return d.ListenAndServe(ctx)
}

// Set by release builds
var releaseVersion, releaseCommit string

// Reads the release or module version from build info, adding the commit unless the version already names it
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
	if releaseVersion != "" {
		version = releaseVersion
	}
	if releaseCommit != "" {
		revision = releaseCommit
		if len(revision) > 7 {
			revision = revision[:7]
		}
	}
	if revision != "" && !strings.Contains(version, revision) {
		version += " " + revision
	}
	return version
}
