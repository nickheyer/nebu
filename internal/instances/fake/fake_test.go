package fake

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/launch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

// The runtime under test
var fakeRuntime = Runtime{}

// A free loopback port
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// Launches a fake process of the kind under the name on a free port, stopped at cleanup
func start(t *testing.T, l *Launcher, name, kind string, extra ...string) (*Proc, int) {
	t.Helper()
	port := freePort(t)
	args := append([]string{"--kind", kind, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--name", name}, extra...)
	h, err := l.Launch(context.Background(), launch.Spec{Command: "fake", Args: args, LogPath: filepath.Join(t.TempDir(), "logs", name+".log")})
	if err != nil {
		t.Fatal(err)
	}
	p := h.(*Proc)
	t.Cleanup(func() { p.Stop(context.Background(), time.Second) })
	return p, port
}

func get(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func post(t *testing.T, url string, body any) (int, []byte) {
	t.Helper()
	data, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// An http process answers health, chat completions, the model list, adapters, sleep and the vLLM metrics until stopped
func TestLauncherServesHTTP(t *testing.T) {
	l := New()
	p, port := start(t, l, "solo", "http", "--served", "public", "--transport", "rdma")
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	if p.Pid() <= 0 || p.Served != "public" || p.Kind != "http" || p.Err() != nil || l.Proc("solo") != p || l.Proc("none") != nil {
		t.Fatalf("proc %+v", p)
	}
	if code, _ := get(t, base+"/health"); code != http.StatusOK {
		t.Fatalf("health %d", code)
	}
	l.SetHealthy("solo", false)
	if code, _ := get(t, base+"/health"); code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy %d", code)
	}
	l.SetHealthy("solo", true)
	code, body := post(t, base+"/v1/chat/completions", map[string]any{"model": "public"})
	var answer map[string]any
	json.Unmarshal(body, &answer)
	if code != http.StatusOK || answer["model"] != "public" || p.Chats.Load() != 1 {
		t.Fatalf("chat %d %s chats %d", code, body, p.Chats.Load())
	}
	if _, ok := answer["kv_transfer_params"]; ok {
		t.Fatal("a solo process hands nothing off")
	}
	if code, _ := post(t, base+"/v1/load_lora_adapter", map[string]string{"lora_name": "", "lora_path": "/x"}); code != http.StatusBadRequest {
		t.Fatalf("adapter without a name %d", code)
	}
	if code, _ := post(t, base+"/v1/load_lora_adapter", map[string]string{"lora_name": "sql", "lora_path": "/adapters/sql"}); code != http.StatusOK {
		t.Fatalf("load adapter %d", code)
	}
	_, body = get(t, base+"/v1/models")
	if !strings.Contains(string(body), `"id":"sql"`) || !strings.Contains(string(body), `"root":"/adapters/sql"`) || !strings.Contains(string(body), `"parent":"public"`) {
		t.Fatalf("models %s", body)
	}
	_, body = get(t, base+"/metrics")
	if !strings.Contains(string(body), `running_lora_adapters="sql"`) || !strings.Contains(string(body), `vllm:num_requests_running{model_name="public"} 0`) {
		t.Fatalf("metrics %s", body)
	}
	if code, _ := post(t, base+"/v1/unload_lora_adapter", map[string]string{"lora_name": "sql"}); code != http.StatusOK {
		t.Fatalf("unload adapter %d", code)
	}
	if code, _ := post(t, base+"/v1/unload_lora_adapter", map[string]string{"lora_name": "sql"}); code != http.StatusNotFound {
		t.Fatalf("unload a missing adapter %d", code)
	}
	if fakeRuntime.Adapters(base, nil) != nil || fakeRuntime.Sleeper(base, nil) != nil {
		t.Fatal("a run without the switches has no adapter or sleep client")
	}
	adapters := fakeRuntime.Adapters(base, map[string]string{ParamLoRA: "true"})
	if err := adapters.Load(context.Background(), "math", "/adapters/math"); err != nil {
		t.Fatal(err)
	}
	if list, err := adapters.List(context.Background()); err != nil || len(list) != 1 || list[0].Name != "math" {
		t.Fatalf("adapters %v %v", list, err)
	}
	if err := adapters.Unload(context.Background(), "math"); err != nil {
		t.Fatal(err)
	}
	sleeper := fakeRuntime.Sleeper(base, map[string]string{ParamSleep: "true"})
	if err := sleeper.Sleep(context.Background()); err != nil || !p.Asleep() || p.Sleeps.Load() != 1 {
		t.Fatalf("sleep %v asleep %v", err, p.Asleep())
	}
	if err := sleeper.Wake(context.Background()); err != nil || p.Asleep() || p.Wakes.Load() != 1 {
		t.Fatalf("wake %v asleep %v", err, p.Asleep())
	}
	lines := p.Log().Tail(0)
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "weights=") || lines[1] != "transport: rdma" {
		t.Fatalf("log %v", lines)
	}
	if l.Launches("solo") != 1 || strings.Join(l.Order(), ",") != "solo" || len(l.StopOrder()) != 0 {
		t.Fatalf("launches %d order %v stops %v", l.Launches("solo"), l.Order(), l.StopOrder())
	}
	if err := p.Stop(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the process did not end")
	}
	if p.Err() == nil || strings.Join(l.StopOrder(), ",") != "solo" || p.StoppedAt.IsZero() {
		t.Fatalf("after stop err %v stops %v", p.Err(), l.StopOrder())
	}
	if _, err := http.Get(base + "/health"); err == nil {
		t.Fatal("the server outlived the process")
	}
}

// A prefill seat hands off connector parameters unless told not to, and a hello seat answers the rpc hello and counts bytes
func TestLauncherPrefillAndHello(t *testing.T) {
	l := New()
	_, prefill := start(t, l, "pre", "prefill")
	_, body := post(t, "http://127.0.0.1:"+strconv.Itoa(prefill)+"/v1/chat/completions", map[string]any{"kv_transfer_params": map[string]any{}})
	if !strings.Contains(string(body), `"remote_engine_id":"fake"`) {
		t.Fatalf("prefill answer %s", body)
	}
	_, quiet := start(t, l, "quiet", "prefill", "--no-handoff")
	_, body = post(t, "http://127.0.0.1:"+strconv.Itoa(quiet)+"/v1/chat/completions", map[string]any{"kv_transfer_params": map[string]any{}})
	if strings.Contains(string(body), "kv_transfer_params") {
		t.Fatalf("a seat told not to hand off did: %s", body)
	}
	hello, port := start(t, l, "stage", "hello")
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var msg [9]byte
	msg[0] = rpcCmdHello
	binary.LittleEndian.PutUint64(msg[1:], rpcHelloCaps)
	conn.Write(msg[:])
	conn.Write(make([]byte, rpcHelloCaps))
	var size [8]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil || binary.LittleEndian.Uint64(size[:]) != 4+rpcHelloCaps {
		t.Fatalf("hello size %v %v", size, err)
	}
	answer := make([]byte, 4+rpcHelloCaps)
	if _, err := io.ReadFull(conn, answer); err != nil || answer[0] != rpcMajor {
		t.Fatalf("hello answer %v %v", answer, err)
	}
	data, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	data.Write(make([]byte, 100))
	data.Close()
	deadline := time.Now().Add(5 * time.Second)
	for hello.Received.Load() != 100 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if hello.Received.Load() != 100 {
		t.Fatalf("received %d", hello.Received.Load())
	}
	// A head streams its buffer to the stage and logs the size it placed there
	head, _ := start(t, l, "head", "http", "--peer", "127.0.0.1:"+strconv.Itoa(port))
	deadline = time.Now().Add(5 * time.Second)
	for hello.Received.Load() != 100+StreamBytes && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if hello.Received.Load() != 100+StreamBytes || len(fakeRuntime.Measure(head.Log().Tail(0))) != 1 {
		t.Fatalf("streamed %d log %v", hello.Received.Load(), head.Log().Tail(0))
	}
}

// Failures play as the runtime sees them, a boom line, a port taken once, and a crash on demand
func TestLauncherFailures(t *testing.T) {
	l := New()
	ctx := context.Background()
	wait := func(p *Proc) {
		t.Helper()
		select {
		case <-p.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the process did not end")
		}
	}
	boom, _ := start(t, l, "boom", "http", "--fail")
	wait(boom)
	if lines := boom.Log().Tail(0); lines[len(lines)-1] != BoomLine || boom.Err() == nil {
		t.Fatalf("boom log %v err %v", lines, boom.Err())
	}
	taken, _ := start(t, l, "taken", "http", "--port-taken", "1")
	wait(taken)
	if lines := taken.Log().Tail(0); !strings.Contains(lines[len(lines)-1], "address already in use") {
		t.Fatalf("port taken log %v", lines)
	}
	again, port := start(t, l, "taken", "http", "--port-taken", "1")
	if code, _ := get(t, "http://127.0.0.1:"+strconv.Itoa(port)+"/health"); code != http.StatusOK || again.Err() != nil || l.Launches("taken") != 2 {
		t.Fatalf("second launch health %d err %v launches %d", code, again.Err(), l.Launches("taken"))
	}
	if !l.Crash("taken") || again.Err() == nil || l.Crash("none") {
		t.Fatal("crash on demand")
	}
	if lines := again.Log().Tail(0); lines[len(lines)-1] != BoomLine {
		t.Fatalf("crash log %v", lines)
	}
	if _, err := l.Launch(ctx, launch.Spec{Command: "fake", Args: []string{"--name", "x"}, LogPath: filepath.Join(t.TempDir(), "x.log")}); err == nil {
		t.Fatal("a launch without a kind must fail")
	}
	busy := freePort(t)
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(busy))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	held, err := l.Launch(ctx, launch.Spec{Command: "fake", Args: []string{"--kind", "http", "--host", "127.0.0.1", "--port", strconv.Itoa(busy), "--name", "busy"}, LogPath: filepath.Join(t.TempDir(), "busy.log")})
	if err != nil {
		t.Fatal(err)
	}
	wait(held.(*Proc))
	if lines := held.Log().Tail(0); held.Err() == nil || !strings.Contains(lines[len(lines)-1], "('127.0.0.1', "+strconv.Itoa(busy)+"): address already in use") {
		t.Fatalf("a port something else holds exits the process with the bind line: %v err %v", lines, held.Err())
	}
	stopped, _ := start(t, l, "stopped", "http")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := stopped.Stop(cancelled, time.Second); err == nil {
		t.Fatal("a stop whose context ended must say so")
	}
	wait(stopped)
	if strings.Join(l.Order(), ",") != "boom,taken,taken,busy,stopped" {
		t.Fatalf("order %v", l.Order())
	}
}

func params(kv map[string]any) estimate.Params {
	p := estimate.Params{}
	for k, v := range kv {
		p[k] = v
	}
	return p
}

// The runtime builds commands the launcher understands, per rank and per seat role, and reads its own logs back
func TestRuntimeCommands(t *testing.T) {
	rt := Runtime{}
	if rt.ID() != RuntimeID || rt.Formats()[0] != Format || rt.API() != v1.ApiFlavor_API_FLAVOR_OPENAI || rt.Kind() != v1.ModelKind_MODEL_KIND_LANGUAGE || rt.Metrics() == nil || len(rt.Params()) == 0 || len(rt.Shapes(nil)) != 6 {
		t.Fatal("runtime descriptor")
	}
	cmd, err := rt.Launch(runtimes.Launch{Name: "m", Host: "127.0.0.1", Port: 9000, Params: params(map[string]any{ParamFail: true, ParamPortTaken: int64(2)})})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(cmd.Args, " ")
	if cmd.Command != "fake" || cmd.Port != 9000 || cmd.Env["FAKE_TOKEN"] != Token || !strings.Contains(args, "--kind http") || !strings.Contains(args, "--name m ") || !strings.Contains(args, "--fail") || !strings.Contains(args, "--port-taken 2") || strings.Contains(args, "--served") {
		t.Fatalf("solo command %+v", cmd)
	}
	ranked, err := rt.Launch(runtimes.Launch{Name: "m", Host: "127.0.0.1", Port: 9000, Rank: 1, Params: params(map[string]any{ParamRanks: int64(2)})})
	if err != nil || ranked.Port != 9001 || !strings.Contains(strings.Join(ranked.Args, " "), "--served m") || !strings.Contains(strings.Join(ranked.Args, " "), "--name "+RankName("m", 1)) {
		t.Fatalf("ranked command %+v %v", ranked, err)
	}
	if rt.Ranks(map[string]string{ParamRanks: "3"}) != 3 || rt.Ranks(nil) != 1 || rt.PortSpan(runtimes.Launch{Params: params(map[string]any{ParamRanks: int64(2)})}) != 2 {
		t.Fatal("ranks and port span")
	}
	if rt.Capacity(map[string]string{ParamMaxSeqs: "7"}) != 7 || rt.MetricsPort(nil, 5) != 5 || rt.GRPCPort(map[string]string{ParamGRPC: "true"}, 5) != 6 || rt.GRPCPort(nil, 5) != 0 || rt.AppProtocol(map[string]string{ParamGRPC: "true"}) != rt.AppProtocol(map[string]string{ParamGRPC: "1"}) {
		t.Fatal("ports and capacity")
	}
	measured := rt.Measure([]string{"weights=1000", "noise", "weights=x"})
	if len(measured) != 1 || measured[0].GetBytes() != 1000 || measured[0].GetKey() != "weights" {
		t.Fatalf("measure %v", measured)
	}
	if rt.Transport([]string{"transport: rdma", "transport: sockets"}) != "rdma" || rt.Transport(nil) != "" {
		t.Fatal("transport line")
	}
	if len(rt.Triage()) != 1 || rt.Triage()[0].ID() != "fake" || rt.PortRule().ID == "" {
		t.Fatal("triage rules")
	}
	seat := &v1.SeatSpec{Name: "chain", Shape: v1.Shape_SHAPE_RELAY, Role: runtimes.RolePrefill, Rank: 0, Peers: []*v1.SeatPeer{{Role: runtimes.RoleDecode, Address: "127.0.0.1:1"}}}
	cmd, err = rt.LaunchSeat(runtimes.Launch{Name: "chain", Host: "127.0.0.1", Port: 9100, Seat: seat, Params: params(map[string]any{ParamFailRole: FailHandoff, ParamSlowRole: runtimes.RolePrefill, ParamTransport: "sockets"})})
	if err != nil {
		t.Fatal(err)
	}
	args = strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "--kind prefill") || !strings.Contains(args, "--no-handoff") || !strings.Contains(args, "--slow") || !strings.Contains(args, "--transport sockets") || !strings.Contains(args, "--served chain") || cmd.Params["kind"] != "prefill" {
		t.Fatalf("prefill seat %+v", cmd)
	}
	if _, err := rt.LaunchSeat(runtimes.Launch{Name: "chain", Host: "127.0.0.1", Port: 9100, Seat: &v1.SeatSpec{Name: "chain", Shape: v1.Shape_SHAPE_RELAY, Role: "nonsense"}}); err == nil {
		t.Fatal("an unknown role must fail")
	}
	if RankName("m", 0) != "m" || RankName("m", 2) == "m" {
		t.Fatalf("rank names %q %q", RankName("m", 0), RankName("m", 2))
	}
}
