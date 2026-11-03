package installs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The condition of a type on an install, nil when absent
func installCondition(in *v1.Install, typ string) *v1.Condition {
	for _, c := range in.GetStatus().GetConditions() {
		if c.GetType() == typ {
			return c
		}
	}
	return nil
}

// An adopted install carries Accepted and Ready, a vanished binary flips Ready alone, and the status survives the store
func TestInstallStatusFollowsTheHost(t *testing.T) {
	m := buildManager(t)
	ctx := context.Background()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-server")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 1.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	in, err := m.Adopt(ctx, "fake", bin)
	if err != nil {
		t.Fatal(err)
	}
	if in.GetGeneration() != 1 || in.GetStatus().GetObservedGeneration() != 1 {
		t.Fatalf("first record generation %d observed %d", in.GetGeneration(), in.GetStatus().GetObservedGeneration())
	}
	accepted, ready := installCondition(in, ConditionAccepted), installCondition(in, ConditionReady)
	if accepted.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE || accepted.GetReason() != ReasonAccepted || ready.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE || ready.GetReason() != ReasonBinaryPresent {
		t.Fatalf("adopted conditions %v", in.GetStatus())
	}
	stored, err := m.Get(ctx, in.GetId())
	if err != nil || installCondition(stored, ConditionReady).GetReason() != ReasonBinaryPresent || stored.GetGeneration() != 1 {
		t.Fatalf("stored install %v %v", stored, err)
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	m.Refresh(ctx)
	after, err := m.Get(ctx, in.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if c := installCondition(after, ConditionReady); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE || c.GetReason() != ReasonBinaryMissing {
		t.Fatalf("ready after the binary vanished: %v", c)
	}
	if c := installCondition(after, ConditionAccepted); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE || !c.GetLastTransitionTime().AsTime().Equal(accepted.GetLastTransitionTime().AsTime()) {
		t.Fatalf("accepted moved without a transition: %v, was %v", c, accepted)
	}
	if after.GetGeneration() != 1 {
		t.Fatalf("a status change bumped the generation to %d", after.GetGeneration())
	}
	again := &v1.Install{Id: in.GetId(), RuntimeId: "nope", Kind: in.GetKind(), Path: "relative/bin", Dir: dir, CreatedAt: in.GetCreatedAt()}
	if err := m.saveInstall(ctx, again); err != nil {
		t.Fatal(err)
	}
	if again.GetGeneration() != 2 || again.GetStatus().GetObservedGeneration() != 2 {
		t.Fatalf("a changed path kept generation %d observed %d", again.GetGeneration(), again.GetStatus().GetObservedGeneration())
	}
	if c := installCondition(again, ConditionAccepted); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE || c.GetReason() != ReasonUnknownRuntime {
		t.Fatalf("accepted with an unknown runtime: %v", c)
	}
}

func TestNextGeneration(t *testing.T) {
	base := &v1.Install{Id: "i", Path: "/p", Dir: "/", Version: "1", Origin: "o", Kind: v1.InstallKind_INSTALL_KIND_PREBUILT, BuildId: "b", Generation: 4}
	tests := []struct {
		name string
		prev *v1.Install
		in   *v1.Install
		want uint64
	}{
		{name: "first record", in: base, want: 1},
		{name: "unchanged", prev: base, in: base, want: 4},
		{name: "stored before generations", prev: &v1.Install{Path: "/p", Dir: "/", Version: "1", Origin: "o", Kind: v1.InstallKind_INSTALL_KIND_PREBUILT, BuildId: "b"}, in: base, want: 1},
		{name: "new path", prev: base, in: &v1.Install{Path: "/q", Dir: "/", Version: "1", Origin: "o", Kind: v1.InstallKind_INSTALL_KIND_PREBUILT, BuildId: "b"}, want: 5},
		{name: "new version", prev: base, in: &v1.Install{Path: "/p", Dir: "/", Version: "2", Origin: "o", Kind: v1.InstallKind_INSTALL_KIND_PREBUILT, BuildId: "b"}, want: 5},
		{name: "new kind", prev: base, in: &v1.Install{Path: "/p", Dir: "/", Version: "1", Origin: "o", Kind: v1.InstallKind_INSTALL_KIND_BUILT, BuildId: "b"}, want: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextGeneration(tt.prev, tt.in); got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

// The relative path and not executable refusals name their reasons
func TestInstallStatusReasons(t *testing.T) {
	m := buildManager(t)
	ctx := context.Background()
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		in           *v1.Install
		acceptedWant string
		readyWant    string
	}{
		{name: "plain file", in: &v1.Install{Id: "a", RuntimeId: "fake", Path: plain}, acceptedWant: ReasonAccepted, readyWant: ReasonNotExecutable},
		{name: "directory", in: &v1.Install{Id: "b", RuntimeId: "fake", Path: dir}, acceptedWant: ReasonAccepted, readyWant: ReasonNotExecutable},
		{name: "relative path", in: &v1.Install{Id: "c", RuntimeId: "fake", Path: "bin/server"}, acceptedWant: ReasonRelativePath, readyWant: ReasonBinaryMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := m.observe(ctx, tt.in); err != nil {
				t.Fatal(err)
			}
			if got := installCondition(tt.in, ConditionAccepted).GetReason(); got != tt.acceptedWant {
				t.Fatalf("accepted reason %s, want %s", got, tt.acceptedWant)
			}
			if got := installCondition(tt.in, ConditionReady).GetReason(); got != tt.readyWant {
				t.Fatalf("ready reason %s, want %s", got, tt.readyWant)
			}
		})
	}
}
