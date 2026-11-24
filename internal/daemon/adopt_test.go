package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

// The daemon's install of its own binary follows the binary it runs from, under the id it had, while a relay binary a
// person adopted stays the one they pointed at through every start
func TestAdoptSelfFollowsItsOwnBinaryAndLeavesAnAdoptedOne(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	openai, anthropic := runtimes.Relay{Speaks: v1.ApiFlavor_API_FLAVOR_OPENAI}, runtimes.Relay{Speaks: v1.ApiFlavor_API_FLAVOR_ANTHROPIC}
	rts, err := runtimes.New([]runtimes.Runtime{openai, anthropic})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &installs.Manager{DB: store, Runtimes: rts, Defaults: &v1.Builds{}, Host: host.New(nil, nil, time.Minute), Tasks: tasks.New(ctx, log, store, nil), Events: events.New(), Log: log}
	d := &Daemon{Installs: m, Log: log}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary := func(name string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	only := func(runtimeID string) *v1.Install {
		t.Helper()
		list, err := m.List(ctx, runtimeID)
		if err != nil || len(list) != 1 {
			t.Fatalf("installs of %s: %v %v", runtimeID, list, err)
		}
		return list[0]
	}

	// A person adopted a relay binary of their own before the daemon ever started
	mine := binary("nebu-mine")
	adopted, err := m.Adopt(ctx, anthropic.ID(), mine)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		d.adoptSelf(ctx)
		if in := only(anthropic.ID()); in.GetId() != adopted.GetId() || in.GetPath() != mine || in.GetOrigin() != mine {
			t.Fatalf("a relay binary adopted on purpose became %v", in)
		}
	}
	own := only(openai.ID())
	if own.GetPath() != exe || own.GetOrigin() != installs.OriginSelf || own.GetKind() != v1.InstallKind_INSTALL_KIND_ADOPTED {
		t.Fatalf("the daemon's own install %v", own)
	}

	// The daemon runs from another binary than the one its install names
	if _, err := m.Readopt(ctx, own.GetId(), binary("nebu-before"), installs.OriginSelf); err != nil {
		t.Fatal(err)
	}
	d.adoptSelf(ctx)
	if in := only(openai.ID()); in.GetId() != own.GetId() || in.GetPath() != exe || in.GetOrigin() != installs.OriginSelf {
		t.Fatalf("the daemon's own install did not follow its binary: %v", in)
	}

	// An install from before the daemon's own were marked: taken for its own when it names the binary running
	if _, err := m.Readopt(ctx, own.GetId(), exe, ""); err != nil {
		t.Fatal(err)
	}
	d.adoptSelf(ctx)
	if in := only(openai.ID()); in.GetPath() != exe || in.GetOrigin() != installs.OriginSelf {
		t.Fatalf("an unmarked install of the running binary was not taken for the daemon's own: %v", in)
	}
	// Or a binary no longer there
	gone := binary("nebu-gone")
	if _, err := m.Readopt(ctx, own.GetId(), gone, ""); err != nil {
		t.Fatal(err)
	}
	os.Remove(gone)
	d.adoptSelf(ctx)
	if in := only(openai.ID()); in.GetId() != own.GetId() || in.GetPath() != exe || in.GetOrigin() != installs.OriginSelf {
		t.Fatalf("an unmarked install of a binary that is gone was not moved: %v", in)
	}
	// And left alone when it names another binary that is there
	kept := binary("nebu-kept")
	if _, err := m.Readopt(ctx, own.GetId(), kept, ""); err != nil {
		t.Fatal(err)
	}
	d.adoptSelf(ctx)
	if in := only(openai.ID()); in.GetPath() != kept || in.GetOrigin() != kept {
		t.Fatalf("an unmarked install of another binary was overwritten: %v", in)
	}
}
