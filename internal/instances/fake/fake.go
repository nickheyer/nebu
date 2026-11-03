// Package fake runs real processes with fake servers so instance, seat and formation lifecycles run without a model
package fake

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/launch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/triage"
)

// The runtime id the fake registers under and the format it serves
const (
	RuntimeID = "fake"
	Format    = "fake"
	// Bytes a chain head streams to each stage at start
	StreamBytes = 1 << 20
	// The ggml rpc hello command, its capability block, and the version the fake stage answers
	rpcCmdHello  = 14
	rpcHelloCaps = 24
	rpcMajor     = 7
)

// Parameters the fake runtime takes
const (
	// Which seat role fails at start, or handoff for a prefill seat that never hands off
	ParamFailRole = "fail_role"
	// Which seat role is slow to answer its health
	ParamSlowRole = "slow_role"
	// The transport line the seat logs
	ParamTransport = "transport"
	// A part named by a store reference
	ParamExtraPart = "extra_part"
	// A solo run that goes boom at every start
	ParamFail = "fail"
	// Launches of a run that find their port taken before one binds
	ParamPortTaken = "port_taken"
	// A run that serves gRPC on the port after its HTTP port
	ParamGRPC = "grpc"
	// A run that loads and unloads adapters at runtime
	ParamLoRA = "enable_lora"
	// A run that sleeps through its idle window instead of exiting
	ParamSleep = "enable_sleep_mode"
	// Requests one engine runs at once
	ParamMaxSeqs = "max_num_seqs"
	// Comma separated names the instance names a route for beside its own
	ParamAlias = "alias"
	// Data parallel ranks, each a process of its own serving its own port
	ParamRanks = "data_parallel_size"
	// A run whose sleep call fails
	ParamFailSleep = "fail_sleep"
	// Devices each expert layer spreads over
	ParamExpertParallel = "ep_size"
	// The runner and conversion a run pins, vLLM's own params, empty leaving them to the model's architecture
	ParamRunner  = "runner"
	ParamConvert = "convert"
	// What the model's architecture resolves to as vLLM resolves it, a generative model when empty
	ParamArchitecture = "architecture_kind"
	BoomLine          = "boom: the fake process failed"
	// The secret a solo run carries in its env and its command, which records redact
	Token       = "fake-secret-token"
	FailHandoff = "handoff"
)

// A runtime whose processes are fake
type Runtime struct{}

func (Runtime) ID() string                     { return RuntimeID }
func (Runtime) Name() string                   { return "Fake" }
func (Runtime) Description() string            { return "A runtime for lifecycle tests" }
func (Runtime) Formats() []string              { return []string{Format} }
func (Runtime) API() v1.ApiFlavor              { return v1.ApiFlavor_API_FLAVOR_OPENAI }
func (Runtime) Kind() v1.ModelKind             { return v1.ModelKind_MODEL_KIND_LANGUAGE }
func (Runtime) Requirements() []string         { return nil }
func (Runtime) Unmet(*v1.HostProfile) []string { return nil }
func (Runtime) Prepares(string) bool           { return false }
func (Runtime) PrepareTimeout() time.Duration  { return 0 }
func (Runtime) StopGrace() time.Duration       { return time.Second }
func (Runtime) Probes() []runtimes.Probe       { return nil }
func (Runtime) Health() runtimes.Health {
	return runtimes.Health{Path: "/health", Interval: 50 * time.Millisecond, SlowAfter: 10 * time.Second}
}
func (Runtime) Policy() *estimate.Policy {
	return &estimate.Policy{Shapes: &estimate.ShapeFacts{Handoff: estimate.HandoffNIXL}, Deferred: runtimes.VLLMResolved()}
}
func (Runtime) Methods() []runtimes.Method {
	return []runtimes.Method{{ID: "adopt", Description: "Adopt the fake", Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Binaries: []string{"fake"}}}
}
func (Runtime) Params() []*v1.Param {
	return []*v1.Param{
		{Name: ParamFailRole, Type: v1.ParamType_PARAM_TYPE_STRING, Label: "Role that fails"},
		{Name: ParamSlowRole, Type: v1.ParamType_PARAM_TYPE_STRING, Label: "Role slow to answer"},
		{Name: ParamTransport, Type: v1.ParamType_PARAM_TYPE_STRING, Label: "Transport line"},
		{Name: ParamExtraPart, Type: v1.ParamType_PARAM_TYPE_PATH, Label: "Extra part"},
		{Name: ParamFail, Type: v1.ParamType_PARAM_TYPE_BOOL, Label: "Fails at start"},
		{Name: ParamPortTaken, Type: v1.ParamType_PARAM_TYPE_INT, Label: "Launches finding the port taken"},
		{Name: ParamGRPC, Type: v1.ParamType_PARAM_TYPE_BOOL, Label: "Serves gRPC"},
		{Name: ParamLoRA, Type: v1.ParamType_PARAM_TYPE_BOOL, Label: "Adapters at runtime"},
		{Name: ParamSleep, Type: v1.ParamType_PARAM_TYPE_BOOL, Label: "Sleep mode"},
		{Name: ParamMaxSeqs, Type: v1.ParamType_PARAM_TYPE_INT, Label: "Max sequences"},
		{Name: ParamAlias, Type: v1.ParamType_PARAM_TYPE_STRING, Label: "Served name", Description: "model names the API answers to, comma separated"},
		{Name: ParamRanks, Type: v1.ParamType_PARAM_TYPE_INT, Label: "Data parallel ranks"},
		{Name: ParamFailSleep, Type: v1.ParamType_PARAM_TYPE_BOOL, Label: "Sleep fails"},
		{Name: ParamExpertParallel, Type: v1.ParamType_PARAM_TYPE_INT, Label: "Expert parallel width"},
		{Name: ParamRunner, Type: v1.ParamType_PARAM_TYPE_STRING, Label: "Model runner", Choices: []string{"", "generate", "pooling", "draft"}},
		{Name: ParamConvert, Type: v1.ParamType_PARAM_TYPE_STRING, Label: "Model conversion", Choices: []string{"", "none", "embed", "classify"}},
		{Name: ParamArchitecture, Type: v1.ParamType_PARAM_TYPE_STRING, Label: "Architecture kind", Choices: []string{"", ArchPooling, ArchClassifier}},
	}
}
func (Runtime) Prepare(runtimes.Launch) (*runtimes.Command, error) { return nil, nil }

// Recorded params of a launch, each set value as text
func recorded(p estimate.Params) map[string]string {
	out := map[string]string{}
	for k, v := range p {
		switch x := v.(type) {
		case nil:
		case string:
			if x != "" {
				out[k] = x
			}
		case bool:
			if x {
				out[k] = "true"
			}
		default:
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

// Ranks a launch's params name, at least one
func launchRanks(p estimate.Params) int { return int(max(p.Int(ParamRanks), 1)) }

// Ports one rank binds, two when it serves gRPC
func rankSpan(p estimate.Params) int {
	if p.Bool(ParamGRPC) {
		return 2
	}
	return 1
}

// The process name of a rank, rank 0 under the launch's own name
func RankName(name string, rank int) string {
	if rank == 0 {
		return name
	}
	return name + "/dp" + strconv.Itoa(rank)
}

// The port a rank serves on, every rank after the ports of the ranks before it
func rankPort(in runtimes.Launch) (int, error) {
	if ranks := launchRanks(in.Params); in.Rank < 0 || in.Rank >= ranks {
		return 0, fmt.Errorf("%w: rank %d of a launch of %d ranks", runtimes.ErrParam, in.Rank, ranks)
	}
	return in.Port + in.Rank*rankSpan(in.Params), nil
}

// Renders one rank of a solo run on its port with switches that fail, take ports, or serve more
func (Runtime) Launch(in runtimes.Launch) (*runtimes.Command, error) {
	port, err := rankPort(in)
	if err != nil {
		return nil, err
	}
	args := []string{"--kind", "http", "--host", in.Host, "--port", strconv.Itoa(port), "--name", RankName(in.Name, in.Rank), "--token", Token}
	if launchRanks(in.Params) > 1 {
		args = append(args, "--served", in.Name)
	}
	if in.Params.Bool(ParamFail) {
		args = append(args, "--fail")
	}
	if n, _ := in.Params[ParamPortTaken].(int64); n > 0 {
		args = append(args, "--port-taken", strconv.FormatInt(n, 10))
	}
	if in.Params.Bool(ParamFailSleep) {
		args = append(args, "--fail-sleep")
	}
	for _, name := range []string{ParamRunner, ParamConvert, ParamArchitecture} {
		if v := in.Params.Str(name); v != "" {
			args = append(args, "--"+strings.ReplaceAll(name, "_", "-"), v)
		}
	}
	return &runtimes.Command{Command: "fake", Args: args, Env: map[string]string{"FAKE_TOKEN": Token}, Params: recorded(in.Params), Port: port}, nil
}

func (Runtime) Shapes(*v1.Install) []v1.Shape {
	return []v1.Shape{v1.Shape_SHAPE_SOLO, v1.Shape_SHAPE_CHAIN, v1.Shape_SHAPE_RELAY, v1.Shape_SHAPE_LOCKSTEP, v1.Shape_SHAPE_STAGES, v1.Shape_SHAPE_REPLICAS}
}

// Measurements the fake logs as "weights=<bytes>"
func (Runtime) Measure(lines []string) []*v1.Measurement {
	var out []*v1.Measurement
	for _, line := range lines {
		if rest, ok := strings.CutPrefix(line, "weights="); ok {
			if n, err := strconv.ParseUint(strings.TrimSpace(rest), 10, 64); err == nil {
				out = append(out, &v1.Measurement{Key: "weights", Bytes: n, Line: line})
			}
		}
	}
	return out
}

// The transport the fake names on its transport log line, rdma or sockets
func (Runtime) Transport(lines []string) string {
	for _, line := range lines {
		if rest, ok := strings.CutPrefix(line, "transport: "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func (Runtime) Triage() []triage.Set { return []triage.Set{boomSet{}} }

// One rule matching a boom line
type boomSet struct{}

func (boomSet) ID() string          { return "fake" }
func (boomSet) Description() string { return "The fake process failed" }
func (boomSet) Rules() []triage.Rule {
	return []triage.Rule{{ID: "boom", Summary: "the fake process went boom", Hint: "run it with less boom", Match: func(line string) (map[string]string, bool) {
		return nil, strings.HasPrefix(line, "boom:")
	}}}
}

// The vLLM gauge names, which the fake server exports
func (Runtime) Metrics() *v1.MetricNames { return runtimes.VLLM{}.Metrics() }

func (Runtime) MetricsPort(_ map[string]string, port int) int { return port }

// gRPC when the run asked for it
func (Runtime) AppProtocol(params map[string]string) v1.AppProtocol {
	if on(params, ParamGRPC) {
		return v1.AppProtocol_APP_PROTOCOL_GRPC
	}
	return v1.AppProtocol_APP_PROTOCOL_HTTP
}

// The port after the HTTP port when the run serves gRPC
func (Runtime) GRPCPort(params map[string]string, port int) int {
	if on(params, ParamGRPC) {
		return port + 1
	}
	return 0
}

// Every rank's ports, two for a gRPC rank and one otherwise
func (Runtime) PortSpan(in runtimes.Launch) int { return launchRanks(in.Params) * rankSpan(in.Params) }

// The data parallel ranks the recorded params name, at least one
func (Runtime) Ranks(params map[string]string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(params[ParamRanks]))
	return max(n, 1)
}

// The expert parallel width the recorded params name, at least one
func (Runtime) ExpertParallel(params map[string]string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(params[ParamExpertParallel]))
	return max(n, 1)
}

// Sequences the run caps itself at, zero until set
func (Runtime) Capacity(params map[string]string) uint32 {
	n, _ := strconv.ParseUint(strings.TrimSpace(params[ParamMaxSeqs]), 10, 32)
	return uint32(n)
}

// Zero since the fake reports its blocks through the cache config gauge
func (Runtime) KVCapacity(map[string]string, *v1.MemoryPlan) uint32 { return 0 }

// Named loads and unloads against the fake server when the run turned adapters on
func (Runtime) Adapters(endpoint string, params map[string]string) runtimes.AdapterClient {
	if !on(params, ParamLoRA) {
		return nil
	}
	return adapterClient{base: endpoint}
}

// Sleep and wake against the fake server when the run turned sleep mode on
func (Runtime) Sleeper(endpoint string, params map[string]string) runtimes.SleepClient {
	if !on(params, ParamSleep) {
		return nil
	}
	return sleepClient{base: endpoint}
}

// The context the params name on the host, with the modalities vLLM's runner, conversion and tasks give
func (Runtime) Inventory(in runtimes.Launch, params map[string]string) (*v1.RuntimeInventory, error) {
	n, _ := strconv.ParseUint(params["n_ctx"], 10, 32)
	modalities, err := runtimes.VLLMModalities(in.Descriptor, params)
	if err != nil {
		return nil, err
	}
	return &v1.RuntimeInventory{ContextLength: uint32(n), Quantization: in.Descriptor.GetPrecision().GetLabel(), Accelerator: "cpu", Modalities: modalities, ThroughputClass: runtimes.ThroughputHost}, nil
}

// Architecture kinds the fake model resolves to as vLLM resolves a registry pooling model and a sequence classifier
const (
	ArchPooling    = "pooling"
	ArchClassifier = "classifier"
)

// The runner, conversion and tasks vLLM settles on for a model kind and the pinned values, and the lines it logs doing so
func vllmResolution(arch, runner, convert string) (lines []string, ok bool) {
	if runner == "" {
		switch {
		case convert != "" && convert != "none":
			runner = "pooling"
		case arch == ArchPooling || arch == ArchClassifier:
			runner = "pooling"
		default:
			runner = "generate"
		}
		if runner != "generate" {
			lines = append(lines, "INFO [model.py:1338] Resolved `--runner auto` to `--runner "+runner+"`. Pass the value explicitly to silence this message.")
		}
	}
	if convert == "" {
		switch {
		case runner != "pooling" || arch == ArchPooling:
			convert = "none"
		case arch == ArchClassifier:
			convert = "classify"
		default:
			convert = "embed"
		}
		if convert != "none" {
			lines = append(lines, "INFO [model.py:1390] Resolved `--convert auto` to `--convert "+convert+"`. Pass the value explicitly to silence this message.")
		}
	}
	tasks := ""
	switch {
	case runner != "pooling":
		tasks = "('generate',)"
	case convert == "embed", convert == "none" && arch == ArchPooling:
		tasks = "('embed', 'token_embed')"
	case convert == "classify", convert == "none" && arch == ArchClassifier:
		tasks = "('classify', 'token_classify')"
	default:
		return append(lines, "ValueError: the model has no pooler for --runner pooling --convert none"), false
	}
	return append(lines, "INFO [entry.py:135] Supported tasks: "+tasks), true
}

// Python servers name the address they failed to bind as ('host', port)
var bindRe = regexp.MustCompile(`\(\s*'[^']*'\s*,\s*(\d+)\s*\)`)

// The bind failure line a fake process logs when its port was taken
func (Runtime) PortRule() triage.Rule {
	return triage.Rule{ID: "port-in-use", Summary: "the fake found its port taken", Hint: "reserve another port", Match: func(line string) (map[string]string, bool) {
		if !strings.Contains(line, "address already in use") {
			return nil, false
		}
		if m := bindRe.FindStringSubmatch(line); m != nil {
			return map[string]string{"port": m[1]}, true
		}
		return nil, true
	}}
}

// Whether a recorded switch is on
func on(params map[string]string, name string) bool {
	switch strings.ToLower(strings.TrimSpace(params[name])) {
	case "true", "on", "1":
		return true
	}
	return false
}

// Chain stages and a head, a relay pair, lockstep ranks, diffusion stages, replicas and a solo head
func (Runtime) Roles(shape v1.Shape) []runtimes.Role {
	hello := runtimes.RoleHealth{Kind: runtimes.HealthHello, Interval: 50 * time.Millisecond, SlowAfter: 10 * time.Second}
	httpHealth := runtimes.RoleHealth{Kind: runtimes.HealthHTTP}
	switch shape {
	case v1.Shape_SHAPE_CHAIN:
		return []runtimes.Role{
			{Name: runtimes.RoleStage, Phase: 1, Files: runtimes.FilesNone, Health: hello, Listens: true, Single: true, Helper: true, ConnectsFrom: []string{runtimes.RoleHead}},
			{Name: runtimes.RoleHead, Phase: 2, Files: runtimes.FilesWeights, Health: httpHealth, Head: true, ConnectsFrom: []string{runtimes.RoleConductor}},
		}
	case v1.Shape_SHAPE_RELAY:
		return []runtimes.Role{
			{Name: runtimes.RolePrefill, Phase: 1, Files: runtimes.FilesWeights, Health: httpHealth, Listens: true, ConnectsFrom: []string{runtimes.RoleConductor}},
			{Name: runtimes.RoleDecode, Phase: 1, Files: runtimes.FilesWeights, Health: runtimes.RoleHealth{Kind: runtimes.HealthCanary}, Listens: true, Head: true, ConnectsFrom: []string{runtimes.RoleConductor}},
		}
	case v1.Shape_SHAPE_LOCKSTEP:
		return []runtimes.Role{
			{Name: runtimes.RoleHead, Phase: 1, Files: runtimes.FilesWeights, Health: httpHealth, Rendezvous: true, Head: true, ConnectsFrom: []string{runtimes.RoleConductor, runtimes.RoleRank}},
			{Name: runtimes.RoleRank, Phase: 1, Files: runtimes.FilesWeights, Health: runtimes.RoleHealth{Kind: runtimes.HealthProcess}, Rendezvous: true, ConnectsFrom: []string{runtimes.RoleHead}},
		}
	case v1.Shape_SHAPE_STAGES:
		return []runtimes.Role{
			{Name: runtimes.RoleDenoiser, Phase: 1, Files: runtimes.FilesNone, Health: hello, Listens: true, Single: true, Helper: true, ConnectsFrom: []string{runtimes.RoleHead}},
			{Name: runtimes.RoleHead, Phase: 2, Files: runtimes.FilesParts, Health: httpHealth, Head: true, ConnectsFrom: []string{runtimes.RoleConductor}},
		}
	case v1.Shape_SHAPE_REPLICAS:
		return []runtimes.Role{{Name: runtimes.RoleReplica, Phase: 1, Files: runtimes.FilesWeights, Health: httpHealth, Listens: true, Head: true, ConnectsFrom: []string{runtimes.RoleConductor}}}
	case v1.Shape_SHAPE_SOLO:
		return []runtimes.Role{{Name: runtimes.RoleHead, Phase: 1, Files: runtimes.FilesWeights, Health: httpHealth, Head: true}}
	}
	return nil
}

// Renders a seat process by its role's health with its address, failure switches, transport line and stage peers
func (r Runtime) LaunchSeat(in runtimes.Launch) (*runtimes.Command, error) {
	seat := in.Seat
	role, err := runtimes.RoleOf(r, seat)
	if err != nil {
		return nil, err
	}
	host := in.Host
	port, err := rankPort(in)
	if err != nil {
		return nil, err
	}
	if launchRanks(in.Params) > 1 && role.Health.Kind != runtimes.HealthHTTP {
		return nil, fmt.Errorf("%w: a %s seat runs one engine and %s asks for %d ranks", runtimes.ErrParam, role.Name, ParamRanks, launchRanks(in.Params))
	}
	kind := "http"
	switch role.Health.Kind {
	case runtimes.HealthHello:
		kind = "hello"
	case runtimes.HealthProcess:
		kind = "process"
	}
	if role.Name == runtimes.RolePrefill {
		kind = "prefill"
	}
	// The process identity is the seat's default instance name, the served name is the formation's
	identity := RankName(fmt.Sprintf("%s/%s%d", seat.GetName(), role.Name, seat.GetRank()), in.Rank)
	args := []string{"--kind", kind, "--host", host, "--port", strconv.Itoa(port), "--name", identity, "--served", in.Name, "--role", role.Name}
	if in.Params.Str(ParamFailRole) == role.Name {
		args = append(args, "--fail")
	}
	if in.Params.Str(ParamFailRole) == FailHandoff && role.Name == runtimes.RolePrefill {
		args = append(args, "--no-handoff")
	}
	if in.Params.Str(ParamSlowRole) == role.Name {
		args = append(args, "--slow")
	}
	if t := in.Params.Str(ParamTransport); t != "" {
		args = append(args, "--transport", t)
	}
	if role.Head {
		for _, p := range seat.GetPeers() {
			if p.GetAddress() != "" && (p.GetRole() == runtimes.RoleStage || p.GetRole() == runtimes.RoleDenoiser) {
				args = append(args, "--peer", p.GetAddress())
			}
		}
	}
	if seat.GetRendezvous() != "" {
		args = append(args, "--rendezvous", seat.GetRendezvous())
	}
	for _, name := range []string{ParamRunner, ParamConvert, ParamArchitecture} {
		if v := in.Params.Str(name); v != "" {
			args = append(args, "--"+strings.ReplaceAll(name, "_", "-"), v)
		}
	}
	if role.Files == runtimes.FilesNone && seat.GetCacheDir() != "" {
		if err := os.MkdirAll(seat.GetCacheDir(), 0o755); err != nil {
			return nil, err
		}
	}
	params := recorded(in.Params)
	params["kind"] = kind
	return &runtimes.Command{Command: "fake", Args: args, Params: params, Port: port}, nil
}

// Answers adapter calls against the fake server's listing
type adapterClient struct{ base string }

func (a adapterClient) List(ctx context.Context) ([]runtimes.Adapter, error) {
	var listing struct {
		Data []struct {
			ID     string  `json:"id"`
			Root   string  `json:"root"`
			Parent *string `json:"parent"`
		} `json:"data"`
	}
	if err := call(ctx, http.MethodGet, a.base+"/v1/models", nil, &listing); err != nil {
		return nil, err
	}
	var out []runtimes.Adapter
	for _, m := range listing.Data {
		if m.Parent != nil {
			out = append(out, runtimes.Adapter{Name: m.ID, Path: m.Root})
		}
	}
	return out, nil
}

func (a adapterClient) Load(ctx context.Context, name, path string) error {
	return call(ctx, http.MethodPost, a.base+"/v1/load_lora_adapter", map[string]string{"lora_name": name, "lora_path": path}, nil)
}

func (a adapterClient) Unload(ctx context.Context, name string) error {
	return call(ctx, http.MethodPost, a.base+"/v1/unload_lora_adapter", map[string]string{"lora_name": name}, nil)
}

// Puts the fake server to sleep and wakes it
type sleepClient struct{ base string }

func (s sleepClient) Sleep(ctx context.Context) error {
	return call(ctx, http.MethodPost, s.base+"/sleep?level=1", nil, nil)
}

func (s sleepClient) Wake(ctx context.Context) error {
	return call(ctx, http.MethodPost, s.base+"/wake_up", nil, nil)
}

// Sends one JSON request and decodes the answer, a status outside 2xx failing with the body
func call(ctx context.Context, method, url string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Launches real shell processes adoption finds by pid, each with a fake server beside it in this process
type Launcher struct {
	mu    sync.Mutex
	procs map[string]*Proc
	// Launch and stop order by name
	Launched []string
	Stopped  []string
	// Port taken failures already played per name
	taken map[string]int
}

// A fake process
type Proc struct {
	Name, Kind string
	// The model name the server answers to
	Served   string
	Args     []string
	cmd      *exec.Cmd
	log      *launch.Log
	file     *os.File
	ln       net.Listener
	srv      *http.Server
	done     chan struct{}
	err      error
	stopOnce sync.Once
	// Chat completions the fake HTTP server answered, bytes the fake hello server took
	Chats    atomic.Int32
	Received atomic.Int64
	// Chat completions answering now, what the running gauge reports
	running atomic.Int32
	// When the process was launched and stopped
	LaunchedAt, StoppedAt time.Time
	mu                    sync.Mutex
	l                     *Launcher
	// Whether the health path fails, the server sleeps, and the adapters it applies by name
	unhealthy bool
	asleep    bool
	adapters  map[string]string
	// Sleep and wake calls the server took
	Sleeps, Wakes atomic.Int32
	// Closed to let held chat completions answer, nil while none is held
	gate chan struct{}
}

// Holds every chat completion until release, the running gauge counting the held ones
func (p *Proc) Hold() (release func()) {
	gate := make(chan struct{})
	p.mu.Lock()
	p.gate = gate
	p.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			if p.gate == gate {
				p.gate = nil
			}
			p.mu.Unlock()
			close(gate)
		})
	}
}

func New() *Launcher { return &Launcher{procs: map[string]*Proc{}, taken: map[string]int{}} }

// The latest process launched under a name, nil when none was
func (l *Launcher) Proc(name string) *Proc {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.procs[name]
}

// Names of the processes launched so far, in order
func (l *Launcher) Order() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.Launched...)
}

// Names of the processes stopped so far, in order
func (l *Launcher) StopOrder() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.Stopped...)
}

// The port the fake server of the process with pid listens on, as the kernel's socket table would list it, none before it binds
func (l *Launcher) Listening(pid int) ([]int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.procs {
		if p.Pid() != pid {
			continue
		}
		select {
		case <-p.done:
			return nil, fmt.Errorf("pid %d is gone", pid)
		default:
		}
		if port := p.Port(); port > 0 {
			return []int{port}, nil
		}
		return nil, nil
	}
	return nil, fmt.Errorf("pid %d is gone", pid)
}

// The port the fake server listens on, zero before it binds
func (p *Proc) Port() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln == nil {
		return 0
	}
	return p.ln.Addr().(*net.TCPAddr).Port
}

// Launches under a name so far
func (l *Launcher) Launches(name string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, have := range l.Launched {
		if have == name {
			n++
		}
	}
	return n
}

func argOf(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func argsOf(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

// Starts a shell ending in the rendered command and the fake server its kind names
func (l *Launcher) Launch(ctx context.Context, spec launch.Spec) (launch.Handle, error) {
	name := argOf(spec.Args, "--name")
	kind := argOf(spec.Args, "--kind")
	if name == "" || kind == "" {
		return nil, errors.New("fake launch: --name and --kind are required")
	}
	if err := os.MkdirAll(dirOf(spec.LogPath), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	shellArgs := append([]string{"-c", "sleep 100000; exit 0", spec.Command}, spec.Args...)
	cmd := exec.Command("sh", shellArgs...)
	if err := cmd.Start(); err != nil {
		file.Close()
		return nil, err
	}
	served := argOf(spec.Args, "--served")
	if served == "" {
		served = name
	}
	p := &Proc{Name: name, Served: served, Kind: kind, Args: spec.Args, cmd: cmd, log: launch.NewLog(200), file: file, done: make(chan struct{}), LaunchedAt: time.Now(), l: l, adapters: map[string]string{}}
	go func() {
		p.err = cmd.Wait()
		p.mu.Lock()
		if p.ln != nil {
			p.ln.Close()
		}
		if p.srv != nil {
			p.srv.Close()
		}
		p.mu.Unlock()
		p.log.Close()
		close(p.done)
	}()
	l.mu.Lock()
	l.procs[name] = p
	l.Launched = append(l.Launched, name)
	launched := len(l.Launched)
	takenSoFar := l.taken[name]
	want, _ := strconv.Atoi(argOf(spec.Args, "--port-taken"))
	takePort := takenSoFar < want
	if takePort {
		l.taken[name]++
	}
	l.mu.Unlock()
	p.write(fmt.Sprintf("weights=%d", 1000*launched))
	if t := argOf(spec.Args, "--transport"); t != "" {
		p.write("transport: " + t)
	}
	host, port := argOf(spec.Args, "--host"), argOf(spec.Args, "--port")
	if takePort {
		p.write(fmt.Sprintf("ERROR: [Errno 98] error while attempting to bind on address ('%s', %s): address already in use", host, port))
		p.crash()
		return p, nil
	}
	if hasArg(spec.Args, "--fail") {
		p.write(BoomLine)
		p.crash()
		return p, nil
	}
	if kind == "http" || kind == "prefill" {
		lines, ok := vllmResolution(argOf(spec.Args, "--architecture-kind"), argOf(spec.Args, "--runner"), argOf(spec.Args, "--convert"))
		for _, line := range lines {
			p.write(line)
		}
		if !ok {
			p.crash()
			return p, nil
		}
	}
	slow := hasArg(spec.Args, "--slow")
	switch kind {
	case "hello":
		if err := p.serveHello(net.JoinHostPort(host, port), slow); err != nil {
			p.bindFailed(host, port, err)
			return p, nil
		}
	case "http", "prefill":
		if err := p.serveHTTP(net.JoinHostPort(host, port), kind == "prefill", slow); err != nil {
			p.bindFailed(host, port, err)
			return p, nil
		}
		for _, peer := range argsOf(spec.Args, "--peer") {
			go p.stream(peer)
		}
	}
	return p, nil
}

// Logs a failed bind as a server process would, the uvicorn line for a taken port, and exits
func (p *Proc) bindFailed(host, port string, err error) {
	if errors.Is(err, syscall.EADDRINUSE) {
		p.write(fmt.Sprintf("ERROR: [Errno 98] error while attempting to bind on address ('%s', %s): address already in use", host, port))
	} else {
		p.write("ERROR: " + err.Error())
	}
	p.crash()
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "."
}

// Appends a line to the process log and its log file
func (p *Proc) write(line string) {
	p.log.Write(line)
	p.mu.Lock()
	if p.file != nil {
		fmt.Fprintln(p.file, line)
		p.file.Sync()
	}
	p.mu.Unlock()
}

// Kills the process as a crash would
func (p *Proc) crash() {
	p.cmd.Process.Kill()
}

// Kills the latest process under a name as a crash would, logging a boom first, and returns whether one ran
func (l *Launcher) Crash(name string) bool {
	p := l.Proc(name)
	if p == nil {
		return false
	}
	p.write(BoomLine)
	p.crash()
	<-p.done
	return true
}

// Makes the latest process under a name fail or pass its health path
func (l *Launcher) SetHealthy(name string, healthy bool) {
	if p := l.Proc(name); p != nil {
		p.mu.Lock()
		p.unhealthy = !healthy
		p.mu.Unlock()
	}
}

// Whether the process sleeps now
func (p *Proc) Asleep() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asleep
}

// Answers hellos with the protocol version and counts every other byte, as a ggml rpc server does
func (p *Proc) serveHello(addr string, slow bool) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				first := make([]byte, 9)
				if _, err := io.ReadFull(conn, first); err != nil {
					return
				}
				if first[0] == rpcCmdHello && binary.LittleEndian.Uint64(first[1:]) == rpcHelloCaps {
					caps := make([]byte, rpcHelloCaps)
					if _, err := io.ReadFull(conn, caps); err != nil {
						return
					}
					if slow {
						select {
						case <-time.After(3 * time.Second):
						case <-p.done:
							return
						}
					}
					var size [8]byte
					binary.LittleEndian.PutUint64(size[:], 4+rpcHelloCaps)
					conn.Write(size[:])
					conn.Write(append([]byte{rpcMajor, 0, 0, 0}, make([]byte, rpcHelloCaps)...))
					return
				}
				p.Received.Add(int64(len(first)))
				n, _ := io.Copy(io.Discard, conn)
				p.Received.Add(n)
			}()
		}
	}()
	return nil
}

// Answers health, the vLLM metrics, chat completions, the model listing, adapters and sleep, a prefill seat handing off connector parameters
func (p *Proc) serveHTTP(addr string, prefill, slow bool) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	started := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		unhealthy := p.unhealthy
		p.mu.Unlock()
		if unhealthy || slow && time.Since(started) < 3*time.Second {
			http.Error(w, "loading", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		running := []string{}
		for name := range p.adapters {
			running = append(running, name)
		}
		p.mu.Unlock()
		sort.Strings(running)
		n := p.running.Load()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE vllm:num_requests_waiting gauge\nvllm:num_requests_waiting{model_name=%q} 0\n", p.Served)
		fmt.Fprintf(w, "# TYPE vllm:num_requests_running gauge\nvllm:num_requests_running{model_name=%q} %d\n", p.Served, n)
		fmt.Fprintf(w, "# TYPE vllm:kv_cache_usage_perc gauge\nvllm:kv_cache_usage_perc{model_name=%q} %g\n", p.Served, float64(n)/16)
		fmt.Fprintf(w, "# TYPE vllm:cache_config_info gauge\nvllm:cache_config_info{block_size=\"16\",num_gpu_blocks=\"1024\"} 1\n")
		fmt.Fprintf(w, "# TYPE vllm:lora_requests_info gauge\nvllm:lora_requests_info{max_lora=\"4\",running_lora_adapters=%q,waiting_lora_adapters=\"\"} 1\n", strings.Join(running, ","))
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		type model struct {
			ID     string  `json:"id"`
			Root   string  `json:"root"`
			Parent *string `json:"parent"`
		}
		list := []model{{ID: p.Served, Root: p.Served}}
		p.mu.Lock()
		for name, path := range p.adapters {
			parent := p.Served
			list = append(list, model{ID: name, Root: path, Parent: &parent})
		}
		p.mu.Unlock()
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": list})
	})
	mux.HandleFunc("/v1/load_lora_adapter", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["lora_name"] == "" {
			http.Error(w, "lora_name required", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		p.adapters[body["lora_name"]] = body["lora_path"]
		p.mu.Unlock()
	})
	mux.HandleFunc("/v1/unload_lora_adapter", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		_, ok := p.adapters[body["lora_name"]]
		delete(p.adapters, body["lora_name"])
		p.mu.Unlock()
		if !ok {
			http.Error(w, "no such adapter", http.StatusNotFound)
		}
	})
	mux.HandleFunc("/sleep", func(w http.ResponseWriter, r *http.Request) {
		p.Sleeps.Add(1)
		if hasArg(p.Args, "--fail-sleep") {
			http.Error(w, "the engine refused to sleep", http.StatusInternalServerError)
			return
		}
		p.mu.Lock()
		p.asleep = true
		p.mu.Unlock()
	})
	mux.HandleFunc("/wake_up", func(w http.ResponseWriter, r *http.Request) {
		p.Wakes.Add(1)
		p.mu.Lock()
		p.asleep = false
		p.mu.Unlock()
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		p.running.Add(1)
		defer p.running.Add(-1)
		p.Chats.Add(1)
		p.mu.Lock()
		gate := p.gate
		p.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
			}
		}
		answer := map[string]any{
			"id": "chatcmpl-fake", "object": "chat.completion", "model": p.Served,
			"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4},
		}
		if prefill {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if _, asked := body["kv_transfer_params"]; asked && !hasArg(p.Args, "--no-handoff") {
				answer["kv_transfer_params"] = map[string]any{"remote_block_ids": []int{1}, "remote_engine_id": "fake", "remote_host": "127.0.0.1", "remote_port": 1}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(answer)
	})
	srv := &http.Server{Handler: mux}
	p.mu.Lock()
	p.ln, p.srv = ln, srv
	p.mu.Unlock()
	go srv.Serve(ln)
	return nil
}

// Streams bytes to a stage as a head does at start, logging the buffer it placed there
func (p *Proc) stream(peer string) {
	conn, err := net.DialTimeout("tcp", peer, 5*time.Second)
	if err != nil {
		p.write("stream to " + peer + " failed: " + err.Error())
		return
	}
	defer conn.Close()
	payload := make([]byte, StreamBytes)
	payload[0] = 1
	if _, err := conn.Write(payload); err != nil {
		p.write("stream to " + peer + " failed: " + err.Error())
		return
	}
	p.write(fmt.Sprintf("load_tensors: RPC0[%s] model buffer size = %.2f MiB", peer, float64(StreamBytes)/(1<<20)))
}

func (p *Proc) Pid() int              { return p.cmd.Process.Pid }
func (p *Proc) Done() <-chan struct{} { return p.done }
func (p *Proc) Log() *launch.Log      { return p.log }
func (p *Proc) Sync()                 {}
func (p *Proc) Err() error {
	select {
	case <-p.done:
		return p.err
	default:
		return nil
	}
}

// Stops the process and its fake server, recording the stop order on the launcher, waiting until ctx ends
func (p *Proc) Stop(ctx context.Context, grace time.Duration) error {
	p.stopOnce.Do(func() {
		p.l.mu.Lock()
		p.l.Stopped = append(p.l.Stopped, p.Name)
		p.l.mu.Unlock()
		p.mu.Lock()
		p.StoppedAt = time.Now()
		srv, ln := p.srv, p.ln
		p.mu.Unlock()
		if srv != nil {
			srv.Close()
		}
		if ln != nil {
			ln.Close()
		}
		p.cmd.Process.Kill()
	})
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
