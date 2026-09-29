package instances

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

// A ggml rpc server takes the hello command with its 24 capability bytes and answers its
// protocol version with its own capabilities behind it
func TestRPCHello(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		req := make([]byte, 9)
		if _, err := io.ReadFull(conn, req); err != nil || req[0] != rpcCmdHello || binary.LittleEndian.Uint64(req[1:]) != rpcHelloCaps {
			return
		}
		caps := make([]byte, rpcHelloCaps)
		if _, err := io.ReadFull(conn, caps); err != nil {
			return
		}
		var size [8]byte
		binary.LittleEndian.PutUint64(size[:], 4+rpcHelloCaps)
		conn.Write(size[:])
		conn.Write(append([]byte{7, 0, 0, 0}, make([]byte, rpcHelloCaps)...))
	}()
	version, err := rpcHello(context.Background(), ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if version != "7.0.0" {
		t.Fatalf("version %q", version)
	}
}

// The guard admits the addresses the role names, one connection when the role says so, and
// counts what it forwards
func TestGuardForwarder(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			conn, err := upstream.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(io.Discard, conn)
			}()
		}
	}()
	g, err := newForwarder(":0", upstream.Addr().String(), []string{"127.0.0.1", "::1"}, true, "stage", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	v4 := net.JoinHostPort("127.0.0.1", strconv.Itoa(g.Port()))
	v6 := net.JoinHostPort("::1", strconv.Itoa(g.Port()))
	first, err := net.Dial("tcp", v4)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	payload := make([]byte, 1<<20)
	if _, err := first.Write(payload); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for g.Received() < uint64(len(payload)) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if g.Received() != uint64(len(payload)) {
		t.Fatalf("received %d", g.Received())
	}
	// A second connection from the same address is admitted and counted while the first is open,
	// as ggml's rpc client reconnects for each device query
	second, err := net.Dial("tcp", v4)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := second.Write(payload); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for g.Received() < 2*uint64(len(payload)) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if g.Received() != 2*uint64(len(payload)) || g.Active() != 2 {
		t.Fatalf("received %d over %d connections", g.Received(), g.Active())
	}
	// A connection from another admitted address is refused while the first client's are open
	other := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv6loopback}}
	third, err := other.Dial("tcp", v6)
	if err != nil {
		t.Fatal(err)
	}
	third.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := third.Read(make([]byte, 1)); err == nil || !errors.Is(err, io.EOF) {
		t.Fatalf("the guard closes a second client's connection: %v", err)
	}
	third.Close()
	// Once the first client's connections close, another address is served
	first.Close()
	second.Close()
	deadline = time.Now().Add(5 * time.Second)
	for g.Active() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	fourth, err := other.Dial("tcp", v6)
	if err != nil {
		t.Fatal(err)
	}
	defer fourth.Close()
	if _, err := fourth.Write(payload); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for g.Received() < 3*uint64(len(payload)) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if g.Received() != 3*uint64(len(payload)) {
		t.Fatalf("the next client is served: %d", g.Received())
	}
}

// A seat pins the plan's bare device ids on this node and leaves the ids qualified by another
// node to the runtime's rendering
func TestSeatDevicesPinLocalOnly(t *testing.T) {
	profile := &v1.HostProfile{Devices: []*v1.Device{{Id: "cuda0"}, {Id: "cuda1"}}}
	pinned := seatDevices(profile, []string{"node-b/cuda0", "cuda1"})
	if len(pinned) != 1 || pinned[0].GetId() != "cuda1" {
		t.Fatalf("pinned %v", pinned)
	}
	if seatDevices(profile, nil) != nil {
		t.Fatal("no ids pins every device")
	}
}

// A probe answering with the devices and pools a test gives it
type memoryProbe struct{ res host.Result }

func (memoryProbe) ID() string                        { return "memory" }
func (memoryProbe) Description() string               { return "devices and pools of the test" }
func (memoryProbe) Runs(string, string) bool          { return true }
func (p memoryProbe) Run(context.Context) host.Result { return p.res }

// A seat launches only when every device the plan names still has room for the bytes the plan
// puts on it, and the host pool for what the plan keeps in host memory; a forced run launches past
// the refusal
func TestSeatRefusedWithoutRoom(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	m.Host = host.New([]host.Probe{memoryProbe{host.Result{
		Status:  v1.ProbeStatus_PROBE_STATUS_OK,
		Devices: []*v1.Device{{Id: "cpu0", Kind: v1.DeviceKind_DEVICE_KIND_CPU, Name: "cpu"}, {Id: "gpu0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Name: "small gpu"}},
		Pools: []*v1.MemoryPool{
			{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 64 << 30, FreeBytes: 32 << 30},
			{Id: "gpu0", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "gpu0", TotalBytes: 12 << 30, FreeBytes: 8 << 30},
		},
	}}}, nil, time.Minute)
	run := seatRun(runtimes.RoleStage, 1, false, n.dir)
	run.Seat.Devices, run.Seat.DeviceBytes, run.Seat.DeviceLayers = []string{"other/gpu9", "gpu0"}, []uint64{20 << 30, 9 << 30}, []uint32{10, 5}
	if _, _, err := m.Run(ctx, run); err == nil || !strings.Contains(err.Error(), "puts 9.0 GiB on small gpu of this node, 1.0 GiB over the 8.0 GiB free on it under the runtime's margin") {
		t.Fatalf("the device lacks room: %v", err)
	}
	run.Seat.DeviceBytes[1] = 7 << 30
	run.Seat.Memory = &v1.MemoryPlan{Pools: []*v1.PoolUsage{{PoolId: "host", Kind: v1.PoolKind_POOL_KIND_HOST, UsedBytes: 40 << 30}}}
	if _, _, err := m.Run(ctx, run); err == nil || !strings.Contains(err.Error(), "puts 40.0 GiB on host memory of this node, 8.0 GiB over the 32.0 GiB") {
		t.Fatalf("the host lacks room: %v", err)
	}
	run.Seat.Memory.Pools[0].PoolId = "elsewhere"
	if _, _, err := m.Run(ctx, run); err == nil || !strings.Contains(err.Error(), "host pool elsewhere, which this node's profile does not list") {
		t.Fatalf("an unknown pool: %v", err)
	}
	run.Seat.Devices[1] = "gpu7"
	if _, _, err := m.Run(ctx, run); err == nil || !strings.Contains(err.Error(), "names device gpu7, which this node does not hold") {
		t.Fatalf("an unknown device: %v", err)
	}
	run.Seat.Devices[1], run.Seat.Memory.Pools[0].PoolId = "gpu0", "host"
	run.Seat.DeviceBytes = []uint64{20 << 30}
	if _, _, err := m.Run(ctx, run); err == nil || !strings.Contains(err.Error(), "lists 2 devices and 1 byte shares") {
		t.Fatalf("shares short: %v", err)
	}
	// Within room on both, the seat launches
	run.Seat.DeviceBytes, run.Seat.Memory.Pools[0].UsedBytes = []uint64{20 << 30, 7 << 30}, 30<<30
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_STOPPED)
	// Forced, a seat launches past the refusal
	run.Name, run.Force, run.Seat.DeviceBytes[1] = "chain/forced", true, 9<<30
	in, _, err = m.Run(ctx, run)
	if err != nil {
		t.Fatalf("forced: %v", err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// A seat whose process dies keeps what its log measured beside its triage, so the conductor reads
// the allocations it made and failed to make from its record
func TestFailedSeatKeepsMeasurements(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	run := seatRun(runtimes.RoleStage, 1, false, n.dir)
	run.Params = map[string]string{fake.ParamFailRole: runtimes.RoleStage}
	in, _, err := m.Run(ctx, run)
	if in == nil {
		t.Fatalf("the run is recorded even as it fails: %v", err)
	}
	failed := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_FAILED)
	if len(failed.GetTriage()) == 0 || failed.GetTriage()[0].GetId() != "boom" {
		t.Fatalf("triage %v", failed.GetTriage())
	}
	var weights *v1.Measurement
	for _, ms := range failed.GetMeasurements() {
		if ms.GetKey() == "weights" {
			weights = ms
		}
	}
	if weights == nil || weights.GetBytes() != 1000 {
		t.Fatalf("the failed seat keeps its measurements: %v", failed.GetMeasurements())
	}
}

// Refreshing a seat writes and publishes its record only when the guard counter or the transport
// moved, so a conductor polling every second is quiet until the head streams
func TestRefreshPublishesOnlyChanges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	in, _, err := m.Run(ctx, seatRun(runtimes.RoleStage, 1, false, n.dir))
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	sub := m.Events.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_INSTANCE})
	events := func() int {
		count := 0
		for {
			select {
			case <-sub.Events():
				count++
			case <-time.After(300 * time.Millisecond):
				return count
			}
		}
	}
	for range 3 {
		if _, err := m.Refresh(in.GetId()); err != nil {
			t.Fatal(err)
		}
	}
	if got := events(); got != 0 {
		t.Fatalf("%d instance events for refreshes that changed nothing", got)
	}
	// A hello through the guard moves the counter, and the next refresh publishes it once
	guard := net.JoinHostPort(ready.GetSeat().GetAddress(), strconv.Itoa(int(ready.GetSeat().GetPort())))
	if _, err := rpcHello(ctx, guard); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if g := mustFind(t, m, in.GetId()).guardOf(); g != nil && g.Received() >= 9+rpcHelloCaps {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	refreshed, err := m.Refresh(in.GetId())
	if err != nil || measured(refreshed, GuardReceivedKey) != 9+rpcHelloCaps {
		t.Fatalf("the hello's bytes are measured: %d %v", measured(refreshed, GuardReceivedKey), err)
	}
	if _, err := m.Refresh(in.GetId()); err != nil {
		t.Fatal(err)
	}
	if got := events(); got != 1 {
		t.Fatalf("%d instance events for one change", got)
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

func mustFind(t *testing.T, m *Manager, id string) *instance {
	t.Helper()
	in, err := m.find(id)
	if err != nil {
		t.Fatal(err)
	}
	return in
}
