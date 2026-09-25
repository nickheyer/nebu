package instances

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
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
	g, err := newForwarder("127.0.0.1:0", upstream.Addr().String(), []string{"127.0.0.1"}, true, "stage", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	first, err := net.Dial("tcp", g.Addr())
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
	// A second connection is refused while the first is open.
	second, err := net.Dial("tcp", g.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := second.Read(make([]byte, 1)); err == nil {
		t.Fatal("the second connection is closed by the guard")
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
