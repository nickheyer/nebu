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
	"github.com/nickheyer/nebu/pkg/store"
)

func hostClient(t *testing.T, recent *launch.Log) nebuv1connect.HostServiceClient {
	t.Helper()
	client, _ := hostClientWithStore(t, recent)
	return client
}

// A host client over an empty store, with the store it browses
func hostClientWithStore(t *testing.T, recent *launch.Log) (nebuv1connect.HostServiceClient, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewHostServiceHandler(NewHostService(nil, nil, recent, st)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return nebuv1connect.NewHostServiceClient(srv.Client(), srv.URL), st
}

// Without follow, return recent lines and close the stream.
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

// Follow returns recent lines and streams new lines until disconnect.
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

// File paths list their parent directory with directories first and executables marked.
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
	if entries[0].GetPath() != filepath.Join(dir, "sub") || entries[1].GetPath() != filepath.Join(dir, "llama-server.exe") {
		t.Fatalf("entry paths %q %q", entries[0].GetPath(), entries[1].GetPath())
	}
}

// The trail names every directory from the volume root down to the listed one.
func TestListDirectoryTrailRunsFromTheRoot(t *testing.T) {
	dir := t.TempDir()
	resp, err := hostClient(t, launch.NewLog(1)).ListDirectory(context.Background(), connect.NewRequest(&v1.ListDirectoryRequest{Path: dir}))
	if err != nil {
		t.Fatal(err)
	}
	steps := resp.Msg.GetTrail()
	if len(steps) < 2 {
		t.Fatalf("%v", steps)
	}
	root, last := steps[0], steps[len(steps)-1]
	if filepath.Dir(root.GetPath()) != root.GetPath() || root.GetName() != root.GetPath() {
		t.Fatalf("the trail starts at %q named %q", root.GetPath(), root.GetName())
	}
	if last.GetPath() != dir || last.GetName() != filepath.Base(dir) {
		t.Fatalf("the trail ends at %q named %q", last.GetPath(), last.GetName())
	}
	for i := 1; i < len(steps); i++ {
		if filepath.Dir(steps[i].GetPath()) != steps[i-1].GetPath() {
			t.Fatalf("%q does not sit in %q", steps[i].GetPath(), steps[i-1].GetPath())
		}
	}
}

// No path lists home unless the request starts at the store, and both are offered as places with the volume roots.
func TestListDirectoryStartsAtHomeOrTheStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	client, st := hostClientWithStore(t, launch.NewLog(1))
	resp, err := client.ListDirectory(context.Background(), connect.NewRequest(&v1.ListDirectoryRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetPath() != home {
		t.Fatalf("no path listed %q, not home %q", resp.Msg.GetPath(), home)
	}
	resp, err = client.ListDirectory(context.Background(), connect.NewRequest(&v1.ListDirectoryRequest{Start: v1.DirPlaceKind_DIR_PLACE_KIND_STORE}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetPath() != st.ModelsDir() {
		t.Fatalf("a store start listed %q, not %q", resp.Msg.GetPath(), st.ModelsDir())
	}
	places := resp.Msg.GetPlaces()
	if len(places) < 3 || places[0].GetKind() != v1.DirPlaceKind_DIR_PLACE_KIND_STORE || places[0].GetPath() != st.ModelsDir() || places[1].GetKind() != v1.DirPlaceKind_DIR_PLACE_KIND_HOME || places[1].GetPath() != home {
		t.Fatalf("%v", places)
	}
	for _, p := range places[2:] {
		if p.GetKind() != v1.DirPlaceKind_DIR_PLACE_KIND_VOLUME || filepath.Dir(p.GetPath()) != p.GetPath() {
			t.Fatalf("%v is not a volume root", p)
		}
	}
}

// A store reference lists the directory holding its group.
func TestListDirectoryResolvesAStoreReference(t *testing.T) {
	client, st := hostClientWithStore(t, launch.NewLog(1))
	dir, err := st.GroupDir("huggingface", "org/repo", "Q4_K_M")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "model.gguf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := client.ListDirectory(context.Background(), connect.NewRequest(&v1.ListDirectoryRequest{Path: "store://huggingface/org/repo#Q4_K_M"}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetPath() != dir || len(resp.Msg.GetEntries()) != 1 || resp.Msg.GetEntries()[0].GetName() != "model.gguf" {
		t.Fatalf("path %q entries %v", resp.Msg.GetPath(), resp.Msg.GetEntries())
	}
}

// A reference that names no group is refused, and one that leaves the store is too.
func TestListDirectoryRefusesABadStoreReference(t *testing.T) {
	client := hostClient(t, launch.NewLog(1))
	for _, ref := range []string{"store://huggingface", "store://huggingface/org/repo", "store://huggingface/../../etc#x"} {
		_, err := client.ListDirectory(context.Background(), connect.NewRequest(&v1.ListDirectoryRequest{Path: ref}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%s: %v", ref, err)
		}
	}
}

// Nearest lists the closest directory above a missing path, a missing group included.
func TestListDirectoryNearestClimbsToWhatExists(t *testing.T) {
	client, st := hostClientWithStore(t, launch.NewLog(1))
	dir := t.TempDir()
	resp, err := client.ListDirectory(context.Background(), connect.NewRequest(&v1.ListDirectoryRequest{Path: filepath.Join(dir, "gone", "deeper", "model.gguf"), Nearest: true}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetPath() != dir {
		t.Fatalf("listed %q, not %q", resp.Msg.GetPath(), dir)
	}
	resp, err = client.ListDirectory(context.Background(), connect.NewRequest(&v1.ListDirectoryRequest{Path: "store://huggingface/org/repo#gone", Nearest: true}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetPath() != st.ModelsDir() {
		t.Fatalf("listed %q, not %q", resp.Msg.GetPath(), st.ModelsDir())
	}
}

// Nearest opens the start place for a reference that names nothing.
func TestListDirectoryNearestFallsToTheStartPlace(t *testing.T) {
	client, st := hostClientWithStore(t, launch.NewLog(1))
	resp, err := client.ListDirectory(context.Background(), connect.NewRequest(&v1.ListDirectoryRequest{Path: "store://nothing", Start: v1.DirPlaceKind_DIR_PLACE_KIND_STORE, Nearest: true}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetPath() != st.ModelsDir() {
		t.Fatalf("listed %q, not %q", resp.Msg.GetPath(), st.ModelsDir())
	}
}

// Missing paths return not found.
func TestListDirectoryMissingIsNotFound(t *testing.T) {
	_, err := hostClient(t, launch.NewLog(1)).ListDirectory(context.Background(), connect.NewRequest(&v1.ListDirectoryRequest{Path: filepath.Join(t.TempDir(), "nope")}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("%v", err)
	}
}
