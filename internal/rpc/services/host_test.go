package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// A file lists the directory holding it: directories first, then files by name, binaries marked
func TestListDirectoryListsAroundAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "llama-server.exe"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := hostClient(t, launch.NewLog(1))
	resp, err := client.ListDirectory(context.Background(), connect.NewRequest(&v1.ListDirectoryRequest{Path: filepath.Join(dir, "llama-server.exe")}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetPath() != dir || resp.Msg.GetParent() != filepath.Dir(dir) {
		t.Fatalf("path %q parent %q", resp.Msg.GetPath(), resp.Msg.GetParent())
	}
	entries := resp.Msg.GetEntries()
	if len(entries) != 3 || entries[0].GetName() != "sub" || entries[1].GetName() != "llama-server.exe" || entries[2].GetName() != "README" {
		t.Fatalf("%v", entries)
	}
	if !entries[0].GetDir() || entries[0].GetExecutable() || entries[1].GetDir() || !entries[1].GetExecutable() || entries[2].GetExecutable() {
		t.Fatalf("%v", entries)
	}
}

// A path that is not there answers not found
func TestListDirectoryMissingIsNotFound(t *testing.T) {
	_, err := hostClient(t, launch.NewLog(1)).ListDirectory(context.Background(), connect.NewRequest(&v1.ListDirectoryRequest{Path: filepath.Join(t.TempDir(), "nope")}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("%v", err)
	}
}
