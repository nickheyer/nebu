package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/launch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

func hostClient(t *testing.T, recent *launch.Log) nebuv1connect.HostServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewHostServiceHandler(NewHostService(nil, nil, recent)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return nebuv1connect.NewHostServiceClient(srv.Client(), srv.URL)
}

// Without follow the stream carries the tail and ends
func TestLogsSendsTheTail(t *testing.T) {
	recent := launch.NewLog(10)
	for _, l := range []string{"one", "two", "three"} {
		recent.Write(l)
	}
	stream, err := hostClient(t, recent).Logs(context.Background(), connect.NewRequest(&v1.HostServiceLogsRequest{Tail: 2}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for stream.Receive() {
		got = append(got, stream.Msg().GetLines()...)
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "two" || got[1] != "three" {
		t.Fatalf("%q", got)
	}
}

// Following carries the tail, then each line written after it, until the client goes
func TestLogsFollowsNewLines(t *testing.T) {
	recent := launch.NewLog(10)
	recent.Write("before")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := hostClient(t, recent).Logs(ctx, connect.NewRequest(&v1.HostServiceLogsRequest{Follow: true, Tail: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() {
		t.Fatal(stream.Err())
	}
	if lines := stream.Msg().GetLines(); len(lines) != 1 || lines[0] != "before" {
		t.Fatalf("%q", lines)
	}
	recent.Write("after")
	if !stream.Receive() {
		t.Fatal(stream.Err())
	}
	if lines := stream.Msg().GetLines(); len(lines) != 1 || lines[0] != "after" {
		t.Fatalf("%q", lines)
	}
	cancel()
	if stream.Receive() {
		t.Fatal("the stream should end when the client goes")
	}
	if connect.CodeOf(stream.Err()) != connect.CodeCanceled {
		t.Fatal(stream.Err())
	}
}
