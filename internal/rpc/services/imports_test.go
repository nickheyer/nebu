package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

func imported(id, node string, ready uint32) *v1.PoolImport {
	return &v1.PoolImport{Id: id, Status: &v1.PoolImportStatus{
		RoutingMode: "endpoint",
		Controllers: []*v1.ImportController{{Name: "nebu.dev/mesh-import", ExportingSources: []*v1.ExportingSource{{NodeId: node}}}},
		Endpoints:   []*v1.Endpoint{{Id: id + "-ep", Ready: ready > 0}},
	}}
}

// Imports list and read by id, and a watch sends every import, then each change and each withdrawal as pool events arrive
func TestPoolImportService(t *testing.T) {
	src := &fakeImports{}
	src.set(imported("vllm/hf:a:q4", "n2", 1))
	bus := events.New()
	svc := NewPoolImportService(src, bus)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	list, err := svc.ListPoolImports(ctx, connect.NewRequest(&v1.ListPoolImportsRequest{}))
	if err != nil || len(list.Msg.GetImports()) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	if _, err := svc.GetPoolImport(ctx, connect.NewRequest(&v1.GetPoolImportRequest{Id: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing import %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewPoolImportServiceHandler(svc))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	stream, err := nebuv1connect.NewPoolImportServiceClient(srv.Client(), srv.URL).WatchPoolImports(ctx, connect.NewRequest(&v1.WatchPoolImportsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	next := func() *v1.WatchPoolImportsResponse {
		t.Helper()
		if !stream.Receive() {
			t.Fatalf("stream ended %v", stream.Err())
		}
		return stream.Msg()
	}
	if first := next(); first.GetImport().GetId() != "vllm/hf:a:q4" || first.GetDeleted() {
		t.Fatalf("first %v", first)
	}
	// A pool event with an unchanged import sends nothing, then a change and a new import arrive
	bus.Publish(v1.EventKind_EVENT_KIND_POOL, v1.EventAction_EVENT_ACTION_UPDATED, "x", &v1.Pool{Id: "x"})
	src.set(imported("vllm/hf:a:q4", "n2", 0), imported("vllm/hf:b:q4", "n3", 1))
	bus.Publish(v1.EventKind_EVENT_KIND_POOL, v1.EventAction_EVENT_ACTION_UPDATED, "x", &v1.Pool{Id: "x"})
	seen := map[string]bool{}
	for len(seen) < 2 {
		msg := next()
		if msg.GetDeleted() {
			t.Fatalf("nothing was withdrawn yet: %v", msg)
		}
		seen[msg.GetImport().GetId()] = true
		if msg.GetImport().GetId() == "vllm/hf:a:q4" && msg.GetImport().GetStatus().GetEndpoints()[0].GetReady() {
			t.Fatalf("the changed import arrives changed: %v", msg)
		}
	}
	src.set(imported("vllm/hf:b:q4", "n3", 1))
	bus.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_DELETED, "n2", &v1.Node{Id: "n2"})
	if gone := next(); !gone.GetDeleted() || gone.GetImport().GetId() != "vllm/hf:a:q4" {
		t.Fatalf("withdrawal %v", gone)
	}
}
