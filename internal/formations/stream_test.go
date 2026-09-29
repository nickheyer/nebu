package formations

import (
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The head's log names the buffer it placed on a stage RPC<n>[<address>], n the device's index on
// that server, and the bytes placed there sum over its model buffer lines
func TestHeadStreamed(t *testing.T) {
	lines := []string{
		"load_tensors: offloaded 49/49 layers to GPU",
		"load_tensors: RPC0[192.168.1.89:39823] model buffer size =  4096.00 MiB",
		"load_tensors: RPC0[192.168.1.89:39823] model buffer size =   512.00 MiB",
		"load_tensors: RPC1[10.0.0.3:50052] model buffer size =  1024.00 MiB",
		"load_tensors: RPC[10.0.0.4:50052] model buffer size =   256.00 MiB",
		"load_tensors:        CUDA0 model buffer size =  2048.00 MiB",
		"load_tensors:   CUDA_Host model buffer size =   100.00 MiB",
	}
	if n, ok := headStreamed(lines, "192.168.1.89:39823"); !ok || n != 4608<<20 {
		t.Fatalf("first stage: %d %v", n, ok)
	}
	if n, ok := headStreamed(lines, "10.0.0.3:50052"); !ok || n != 1024<<20 {
		t.Fatalf("second stage: %d %v", n, ok)
	}
	if n, ok := headStreamed(lines, "10.0.0.4:50052"); !ok || n != 256<<20 {
		t.Fatalf("a build naming no index: %d %v", n, ok)
	}
	if _, ok := headStreamed(lines, "10.0.0.5:50052"); ok {
		t.Fatal("an address no buffer names")
	}
	if _, ok := headStreamed(lines[5:], "CUDA0"); ok {
		t.Fatal("a local device is not an rpc buffer")
	}
	if rpcBufferAt("RPCx[10.0.0.3:50052]", "10.0.0.3:50052") || rpcBufferAt("RPC0[10.0.0.3:50052]", "10.0.0.3:5005") {
		t.Fatal("a name that is not an index, or an address that is only a prefix")
	}
}

// Fresh measurements replace the old ones by key and keys only the old list held stay
func TestMergeMeasurements(t *testing.T) {
	old := []*v1.Measurement{{Key: "device.weights", Bytes: 1}, {Key: "guard.received", Bytes: 7}}
	fresh := []*v1.Measurement{{Key: "device.weights", Bytes: 2, Line: "new"}, {Key: "device.failed", Bytes: 3}}
	got := mergeMeasurements(old, fresh)
	if len(got) != 3 || got[0].GetKey() != "device.weights" || got[0].GetBytes() != 2 || got[1].GetKey() != "device.failed" || got[2].GetKey() != "guard.received" || got[2].GetBytes() != 7 {
		t.Fatalf("merged %v", got)
	}
	if len(mergeMeasurements(old, nil)) != 2 {
		t.Fatal("nothing fresh keeps the old")
	}
}
