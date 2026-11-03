// Command nebu-bench records, synthesizes and replays gateway traces and compares scheduler configurations
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/bench"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/stub"
)

const usage = `usage: nebu-bench <command> [flags]

commands:
  record      write the traces a gateway recorded as a JSONL trace
  synthesize  write a generated JSONL trace
  replay      replay a trace against a gateway, once per scheduler configuration
  local       replay a trace against in process stubs the gateway pools, once per scheduler configuration
  compare     put saved reports side by side

run nebu-bench <command> -h for its flags
`

// Wrong command line, reported with the usage
var errUsage = errors.New("unknown or missing command")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
	case errors.Is(err, errUsage):
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "nebu-bench:", err)
		os.Exit(1)
	}
}

// Runs one command, its results on stdout and progress on stderr
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	cmds := map[string]func(context.Context, []string, io.Writer, io.Writer) error{
		"record": record, "synthesize": synthesize, "replay": replay, "local": local, "compare": compare,
	}
	switch name := args[0]; name {
	case "-h", "-help", "--help", "help":
		_, err := io.WriteString(stdout, usage)
		return err
	default:
		cmd, ok := cmds[name]
		if !ok {
			return errUsage
		}
		return cmd(ctx, args[1:], stdout, stderr)
	}
}

// A flag set for one command that reports errors to stderr and refuses positional arguments it does not take
func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("nebu-bench "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// Parses flags and refuses leftover arguments
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	return nil
}

// Repeatable string flag
type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

// Comma separated values with blanks dropped
func split(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Opens a file to write, stdout for a dash
func create(path string, stdout io.Writer) (io.Writer, func() error, error) {
	if path == "-" {
		return stdout, func() error { return nil }, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return f, f.Close, nil
}

// Writes the gateway's chat and generate traces as JSONL
func record(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("record", stderr)
	var c conn
	c.apiFlags(fs)
	route := fs.String("route", "", "one route's traces, every route's when empty")
	limit := fs.Int("limit", 10000, "most recent traces read")
	since := fs.String("since", "", "traces started at or after an RFC 3339 time or a duration ago, such as 1h")
	out := fs.String("out", "-", "trace file to write, - for stdout")
	if err := parse(fs, args); err != nil {
		return err
	}
	opts := bench.RecordOptions{Route: *route, Limit: *limit}
	if *since != "" {
		t, err := sinceTime(*since, time.Now())
		if err != nil {
			return err
		}
		opts.Since = t
	}
	if err := c.resolve(); err != nil {
		return err
	}
	api, err := c.api()
	if err != nil {
		return err
	}
	w, closeOut, err := create(*out, stdout)
	if err != nil {
		return err
	}
	sum, err := bench.Record(ctx, api, w, opts)
	if cerr := closeOut(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "recorded %d of %d listed traces", sum.Recorded, sum.Listed)
	for kind, n := range sum.Excluded {
		fmt.Fprintf(stderr, ", %d %s excluded", n, kind)
	}
	fmt.Fprintln(stderr)
	return nil
}

// An RFC 3339 time, or a duration counted back from now
func sinceTime(v string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return time.Time{}, fmt.Errorf("since %q is neither an RFC 3339 time nor a non-negative duration", v)
	}
	return now.Add(-d), nil
}

// Writes a generated trace
func synthesize(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("synthesize", stderr)
	var spec bench.Spec
	seed := fs.Uint64("seed", 1, "random seed, one seed always writing one trace")
	fs.IntVar(&spec.Requests, "requests", 200, "requests in the trace")
	fs.Float64Var(&spec.Rate, "rate", 10, "mean Poisson arrivals per second")
	fs.StringVar(&spec.Model, "model", "bench-model", "model name every request sends")
	prompt := fs.String("prompt-tokens", "lognormal:256:0.6", "prompt length distribution, fixed:N, uniform:LO:HI, normal:MEAN:SD, lognormal:MEAN:SIGMA or exponential:MEAN")
	output := fs.String("output-tokens", "lognormal:64:0.5", "output length distribution, as prompt-tokens")
	maxPrompt := fs.Int("max-prompt-tokens", 4096, "longest prompt drawn, zero unbounded")
	maxOutput := fs.Int("max-output-tokens", 512, "longest output drawn, zero unbounded")
	fs.Float64Var(&spec.PrefixShare, "prefix-share", 0.3, "fraction of requests opening with a shared system prefix")
	fs.IntVar(&spec.Prefixes, "prefixes", 4, "distinct shared prefixes")
	fs.IntVar(&spec.PrefixTokens, "prefix-tokens", 128, "tokens in each shared prefix")
	fs.IntVar(&spec.FairnessIDs, "fairness-ids", 4, "distinct fairness ids, zero for none")
	fs.Float64Var(&spec.FairnessSkew, "fairness-skew", 1, "Zipf exponent over fairness ids, zero for uniform")
	objectives := fs.String("objectives", "", "comma separated objective header values, names or integer priorities")
	adapters := fs.String("adapters", "", "comma separated adapter names requests may ask for")
	fs.Float64Var(&spec.AdapterShare, "adapter-share", 0, "fraction of requests asking for an adapter")
	out := fs.String("out", "-", "trace file to write, - for stdout")
	if err := parse(fs, args); err != nil {
		return err
	}
	var err error
	if spec.PromptTokens, err = bench.ParseDist(*prompt); err != nil {
		return err
	}
	if spec.OutputTokens, err = bench.ParseDist(*output); err != nil {
		return err
	}
	if *maxPrompt < 0 || *maxOutput < 0 {
		return errors.New("token maximums must not be negative")
	}
	spec.PromptTokens.Max, spec.OutputTokens.Max = *maxPrompt, *maxOutput
	spec.Objectives, spec.Adapters = split(*objectives), split(*adapters)
	reqs, err := bench.Synthesize(*seed, spec)
	if err != nil {
		return err
	}
	w, closeOut, err := create(*out, stdout)
	if err != nil {
		return err
	}
	if err := bench.Write(w, reqs); err != nil {
		closeOut()
		return err
	}
	return closeOut()
}

// Settings replay and local share
type replayFlags struct {
	conn
	trace   string
	speed   float64
	conc    int
	timeout time.Duration
	configs list
	ready   time.Duration
	out     string
}

func (r *replayFlags) register(fs *flag.FlagSet) {
	r.gatewayFlags(fs)
	fs.StringVar(&r.trace, "trace", "", "JSONL trace file to replay")
	fs.Float64Var(&r.speed, "speed", 1, "trace time divisor, 2 replaying twice as fast")
	fs.IntVar(&r.conc, "concurrency", 1024, "requests in flight at once")
	fs.DurationVar(&r.timeout, "timeout", 0, "limit on each request, zero for none")
	fs.Var(&r.configs, "scheduler-config", "scheduler configuration to set on the pool before a run, repeatable for one run each")
	fs.DurationVar(&r.ready, "ready-timeout", time.Minute, "longest wait for the pool to apply a configuration")
	fs.StringVar(&r.out, "out", "", "JSON file to write every report to")
}

// The replay settings and trace, refusing a missing trace or gateway
func (r *replayFlags) load() (bench.Config, []bench.Request, error) {
	if err := r.resolve(); err != nil {
		return bench.Config{}, nil, err
	}
	if r.trace == "" {
		return bench.Config{}, nil, errors.New("replay needs a trace")
	}
	reqs, err := bench.ReadFile(r.trace)
	if err != nil {
		return bench.Config{}, nil, err
	}
	if r.gatewayURL == "" {
		return bench.Config{}, nil, errors.New("replay needs the gateway URL")
	}
	client, err := r.httpClient(r.gatewayURL, false, r.conc)
	if err != nil {
		return bench.Config{}, nil, err
	}
	return bench.Config{Speed: r.speed, Concurrency: r.conc, Timeout: r.timeout, Client: client}, reqs, nil
}

// Prints every report and the comparison, and writes the comparison as JSON when asked
func (r *replayFlags) emit(reports []*bench.Report, stdout io.Writer) error {
	for _, rep := range reports {
		rep.Trace = r.trace
		if err := rep.WriteMarkdown(stdout); err != nil {
			return err
		}
		fmt.Fprintln(stdout)
	}
	cmp, err := bench.Compare(reports...)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "## comparison")
	fmt.Fprintln(stdout)
	if err := cmp.WriteMarkdown(stdout); err != nil {
		return err
	}
	if r.out == "" {
		return nil
	}
	f, err := os.Create(r.out)
	if err != nil {
		return err
	}
	if err := cmp.WriteJSON(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Replays a trace against a gateway, setting each named scheduler configuration on the pool first
func replay(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("replay", stderr)
	var r replayFlags
	r.register(fs)
	pool := fs.String("pool", "", "pool whose scheduler configuration each run sets")
	name := fs.String("name", "replay", "report name of a run that sets no scheduler configuration")
	if err := parse(fs, args); err != nil {
		return err
	}
	named := false
	fs.Visit(func(f *flag.Flag) { named = named || f.Name == "name" })
	switch {
	case len(r.configs) > 0 && *pool == "":
		return errors.New("scheduler-config needs the pool to set it on")
	case len(r.configs) > 0 && named:
		return errors.New("name names a run without scheduler-config, runs with one are named after it")
	case len(r.configs) == 0 && *pool != "":
		return errors.New("pool is only read with scheduler-config")
	}
	cfg, reqs, err := r.load()
	if err != nil {
		return err
	}
	if len(r.configs) == 0 {
		cfg.Name = *name
		rep, err := bench.ReplayRequests(ctx, r.gatewayURL, r.token, reqs, cfg)
		if err != nil {
			return err
		}
		return r.emit([]*bench.Report{rep}, stdout)
	}
	api, err := r.api()
	if err != nil {
		return err
	}
	plan := bench.Plan{Pool: *pool, Configs: r.configs, ReadyTimeout: r.ready, Before: func(_ context.Context, config string) error {
		fmt.Fprintf(stderr, "replaying %d requests under %s\n", len(reqs), config)
		return nil
	}}
	reports, err := bench.RunConfigs(ctx, api, r.gatewayURL, r.token, reqs, cfg, plan)
	if err != nil {
		return err
	}
	return r.emit(reports, stdout)
}

// Starts stubs the gateway pools, replays the trace once per scheduler configuration on fresh stubs, then removes them
func local(ctx context.Context, args []string, stdout, stderr io.Writer) (err error) {
	fs := newFlags("local", stderr)
	var r replayFlags
	r.register(fs)
	var opts bench.LocalOptions
	fs.IntVar(&opts.Stubs, "stubs", 4, "stub backends to start, one to eight")
	fs.StringVar(&opts.Host, "stub-host", "127.0.0.1", "host the stubs listen on and the gateway dials")
	fs.StringVar(&opts.Pool, "pool", "nebu-bench", "id of the pool created for the stubs")
	fs.StringVar(&opts.Runtime, "runtime", "vllm", "runtime the pool names and whose metrics the stubs serve, vllm, sglang or llamacpp")
	sc := &opts.Stub
	fs.StringVar(&sc.Model, "stub-model", "stub-model", "base model the stubs serve")
	mode := fs.String("stub-mode", string(stub.ModeTime), "time sleeps emulated durations, ratio divides them by the speedup")
	fs.Float64Var(&sc.Speedup, "stub-speedup", 1, "wall time divisor in ratio mode")
	fs.DurationVar(&sc.BaseLatency, "stub-base-latency", 15*time.Millisecond, "emulated fixed prefill cost")
	fs.DurationVar(&sc.PrefillPerToken, "stub-prefill-per-token", 100*time.Microsecond, "emulated prefill cost per uncached prompt token")
	fs.DurationVar(&sc.DecodePerToken, "stub-decode-per-token", 15*time.Millisecond, "emulated cost per decoded token")
	fs.Float64Var(&sc.Contention, "stub-contention", 0.02, "step slowdown per other running sequence")
	fs.IntVar(&sc.MaxNumSeqs, "stub-max-num-seqs", 256, "running sequence cap per stub")
	fs.IntVar(&sc.NumGPUBlocks, "stub-num-gpu-blocks", 4096, "KV cache blocks per stub")
	fs.IntVar(&sc.BlockSize, "stub-block-size", 16, "tokens per KV block")
	fs.IntVar(&sc.MaxLoras, "stub-max-loras", 4, "adapter slots per stub, raised to the trace's adapter count")
	if err := parse(fs, args); err != nil {
		return err
	}
	sc.Mode, sc.Records = stub.Mode(*mode), 1
	if sc.Mode == stub.ModeTime && sc.Speedup != 1 {
		return errors.New("stub-speedup applies in ratio mode alone")
	}
	opts.ReadyTimeout = r.ready
	cfg, reqs, err := r.load()
	if err != nil {
		return err
	}
	api, err := r.api()
	if err != nil {
		return err
	}
	l, err := bench.StartLocal(ctx, api, reqs, opts)
	if err != nil {
		return err
	}
	defer func() {
		done, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.ready)
		defer cancel()
		err = errors.Join(err, l.Close(done))
	}()
	fmt.Fprintf(stderr, "pool %s serves stubs %s, routes %v\n", l.Pool(), strings.Join(l.Addresses(), " "), l.Routes())
	configs := []string(r.configs)
	if len(configs) == 0 {
		current, err := currentConfig(ctx, api, l.Pool())
		if err != nil {
			return err
		}
		configs = []string{current}
	}
	plan := bench.Plan{Pool: l.Pool(), Configs: configs, ReadyTimeout: r.ready, Before: func(ctx context.Context, config string) error {
		fmt.Fprintf(stderr, "restarting %d stubs and replaying %d requests under %s\n", opts.Stubs, len(reqs), config)
		return l.Reset(ctx)
	}}
	reports, err := bench.RunConfigs(ctx, api, r.gatewayURL, r.token, reqs, cfg, plan)
	if err != nil {
		return err
	}
	return r.emit(reports, stdout)
}

// Puts saved reports side by side in one table
func compare(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("compare", stderr)
	asJSON := fs.Bool("json", false, "write the comparison as JSON in place of Markdown")
	out := fs.String("out", "-", "file to write, - for stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("compare needs report files, each a report, a list of reports or a comparison")
	}
	var runs []*bench.Report
	for _, path := range fs.Args() {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		reps, err := bench.ReadReports(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		runs = append(runs, reps...)
	}
	c, err := bench.Compare(runs...)
	if err != nil {
		return err
	}
	w, closeOut, err := create(*out, stdout)
	if err != nil {
		return err
	}
	if *asJSON {
		err = c.WriteJSON(w)
	} else {
		err = c.WriteMarkdown(w)
	}
	if cerr := closeOut(); err == nil {
		err = cerr
	}
	return err
}

// The scheduler configuration a pool runs
func currentConfig(ctx context.Context, api nebuv1connect.GatewayServiceClient, pool string) (string, error) {
	got, err := api.GetPool(ctx, connect.NewRequest(&v1.GetPoolRequest{Id: pool}))
	if err != nil {
		return "", fmt.Errorf("reading pool %s: %w", pool, err)
	}
	return got.Msg.GetPool().GetSpec().GetSchedulerConfig(), nil
}
