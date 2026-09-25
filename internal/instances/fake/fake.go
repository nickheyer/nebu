// Package fake is a runtime and a launcher for tests of the seat and formation lifecycle: seats
// are real processes with fake servers beside them, so adoption by pid, hello checks, HTTP health,
// guard listeners, template probes, and relay handoffs all run without a model.
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// Parameters the fake runtime takes: which role fails at start, which role is slow to answer its
// health, the transport line the seat logs, and a part named by a store reference
const (
	ParamFailRole  = "fail_role"
	ParamSlowRole  = "slow_role"
	ParamTransport = "transport"
	ParamExtraPart = "extra_part"
	BoomLine       = "boom: the fake process failed"
	FailHandoff    = "handoff"
)

// A runtime whose seats are fake processes
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
	return runtimes.Health{Path: "/health", Interval: 50 * time.Millisecond, Timeout: 10 * time.Second}
}
func (Runtime) Policy() *estimate.Policy {
	return &estimate.Policy{Shapes: &estimate.ShapeFacts{Handoff: estimate.HandoffNIXL}}
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
	}
}
func (Runtime) Prepare(runtimes.Launch) (*runtimes.Command, error) { return nil, nil }
func (Runtime) Launch(in runtimes.Launch) (*runtimes.Command, error) {
	return &runtimes.Command{Command: "fake", Args: []string{"--kind", "http", "--host", in.Host, "--port", strconv.Itoa(in.Port), "--name", in.Name}, Params: map[string]string{}}, nil
}
func (Runtime) Shapes(*v1.Install) []v1.Shape {
	return []v1.Shape{v1.Shape_SHAPE_SOLO, v1.Shape_SHAPE_CHAIN, v1.Shape_SHAPE_RELAY, v1.Shape_SHAPE_LOCKSTEP, v1.Shape_SHAPE_STAGES}
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

// The transport the fake logs as "transport: rdma" or "transport: sockets"
func (Runtime) Transport(lines []string) string {
	for _, line := range lines {
		if rest, ok := strings.CutPrefix(line, "transport: "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func (Runtime) Triage() []triage.Set { return []triage.Set{boomSet{}} }

// One rule: a boom line
type boomSet struct{}

func (boomSet) ID() string          { return "fake" }
func (boomSet) Description() string { return "The fake process failed" }
func (boomSet) Rules() []triage.Rule {
	return []triage.Rule{{ID: "boom", Summary: "the fake process went boom", Hint: "run it with less boom", Match: func(line string) (map[string]string, bool) {
		return nil, strings.HasPrefix(line, "boom:")
	}}}
}

// Roles of the shapes the fake plays: a chain of hello stages and an HTTP head, a relay pair
// whose decode seat wants the canary, lockstep ranks that rendezvous, and stages with a head
// holding parts
func (Runtime) Roles(shape v1.Shape) []runtimes.Role {
	hello := runtimes.RoleHealth{Kind: runtimes.HealthHello, Interval: 50 * time.Millisecond, Timeout: 10 * time.Second}
	httpHealth := runtimes.RoleHealth{Kind: runtimes.HealthHTTP}
	switch shape {
	case v1.Shape_SHAPE_CHAIN:
		return []runtimes.Role{
			{Name: runtimes.RoleStage, Phase: 1, Files: runtimes.FilesNone, Health: hello, Listens: true, Single: true, ConnectsFrom: []string{runtimes.RoleHead}},
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
			{Name: runtimes.RoleDenoiser, Phase: 1, Files: runtimes.FilesNone, Health: hello, Listens: true, Single: true, ConnectsFrom: []string{runtimes.RoleHead}},
			{Name: runtimes.RoleHead, Phase: 2, Files: runtimes.FilesParts, Health: httpHealth, Head: true, ConnectsFrom: []string{runtimes.RoleConductor}},
		}
	case v1.Shape_SHAPE_SOLO:
		return []runtimes.Role{{Name: runtimes.RoleHead, Phase: 1, Files: runtimes.FilesWeights, Health: httpHealth, Head: true}}
	}
	return nil
}

// Renders a seat: the fake process kind by the role's health, the host and port it binds, the
// parameters that make it fail or dawdle, the transport line it logs, and its stage peers
func (r Runtime) LaunchSeat(in runtimes.Launch) (*runtimes.Command, error) {
	seat := in.Seat
	role, err := runtimes.RoleOf(r, seat)
	if err != nil {
		return nil, err
	}
	host, port := in.Host, in.Port
	if seat.GetExposed() && seat.GetAddress() != "" {
		host, port = seat.GetAddress(), int(seat.GetPort())
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
	args := []string{"--kind", kind, "--host", host, "--port", strconv.Itoa(port), "--name", in.Name, "--role", role.Name}
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
	if role.Files == runtimes.FilesNone && seat.GetCacheDir() != "" {
		if err := os.MkdirAll(seat.GetCacheDir(), 0o755); err != nil {
			return nil, err
		}
	}
	return &runtimes.Command{Command: "fake", Args: args, Params: map[string]string{"kind": kind}}, nil
}

// Launches fake seats: each is a real shell process, so its pid is alive for adoption, with a
// fake server beside it in this process answering hellos or HTTP
type Launcher struct {
	mu    sync.Mutex
	procs map[string]*Proc
	// Launch and stop order by seat name
	Launched []string
	Stopped  []string
	nextPid  int
}

// A fake seat process
type Proc struct {
	Name, Kind string
	Args       []string
	cmd        *exec.Cmd
	log        *launch.Log
	file       *os.File
	ln         net.Listener
	srv        *http.Server
	done       chan struct{}
	err        error
	stopOnce   sync.Once
	// Chat completions the fake HTTP server answered, bytes the fake hello server took
	Chats    atomic.Int32
	Received atomic.Int64
	// When the process was launched and stopped
	LaunchedAt, StoppedAt time.Time
	mu                    sync.Mutex
	l                     *Launcher
}

func New() *Launcher { return &Launcher{procs: map[string]*Proc{}} }

// The process of a seat by name, nil when none was launched
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

// Starts a fake seat: a shell process whose command line ends with the rendered command, so
// adoption finds it by pid, and the fake server the seat's kind names
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
	p := &Proc{Name: name, Kind: kind, Args: spec.Args, cmd: cmd, log: launch.NewLog(200), file: file, done: make(chan struct{}), LaunchedAt: time.Now(), l: l}
	go func() {
		p.err = cmd.Wait()
		p.mu.Lock()
		if p.ln != nil {
			p.ln.Close()
		}
		p.mu.Unlock()
		p.log.Close()
		close(p.done)
	}()
	l.mu.Lock()
	l.procs[name] = p
	l.Launched = append(l.Launched, name)
	l.mu.Unlock()
	p.write(fmt.Sprintf("weights=%d", 1000*(len(l.Launched))))
	if t := argOf(spec.Args, "--transport"); t != "" {
		p.write("transport: " + t)
	}
	if hasArg(spec.Args, "--fail") {
		p.write(BoomLine)
		p.crash()
		return p, nil
	}
	host, port := argOf(spec.Args, "--host"), argOf(spec.Args, "--port")
	slow := hasArg(spec.Args, "--slow")
	switch kind {
	case "hello":
		if err := p.serveHello(net.JoinHostPort(host, port), slow); err != nil {
			p.crash()
			return nil, err
		}
	case "http", "prefill":
		if err := p.serveHTTP(net.JoinHostPort(host, port), kind == "prefill", slow); err != nil {
			p.crash()
			return nil, err
		}
		for _, peer := range argsOf(spec.Args, "--peer") {
			go p.stream(peer)
		}
	}
	return p, nil
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "."
}

// Appends a line to the seat's log and its log file
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

// Kills the process of a seat, as a crash would, and returns whether one was running
func (l *Launcher) Crash(name string) bool {
	p := l.Proc(name)
	if p == nil {
		return false
	}
	p.crash()
	<-p.done
	return true
}

// Answers hello commands with the protocol version and counts every other byte streamed in, as a
// ggml rpc server serving one client
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

// Answers health and chat completions, a prefill seat handing off connector parameters
func (p *Proc) serveHTTP(addr string, prefill, slow bool) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	started := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if slow && time.Since(started) < 3*time.Second {
			http.Error(w, "loading", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		p.Chats.Add(1)
		answer := map[string]any{
			"id": "chatcmpl-fake", "object": "chat.completion", "model": p.Name,
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
	p.srv = &http.Server{Handler: mux}
	go p.srv.Serve(ln)
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
	p.write(fmt.Sprintf("load_tensors: RPC[%s] model buffer size = %.2f MiB", peer, float64(StreamBytes)/(1<<20)))
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

// Stops the process and its fake server, recording the stop order on the launcher
func (p *Proc) Stop(grace time.Duration) error {
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
	<-p.done
	return nil
}
