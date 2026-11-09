package formations

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A relaunch counts on after a predecessor that never served a stretch and starts over after one that did
func TestNextRelaunches(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *timestamppb.Timestamp { return timestamppb.New(now.Add(d)) }
	if got := nextRelaunches(nil); got != 0 {
		t.Fatalf("a fresh run: %d", got)
	}
	if got := nextRelaunches(&v1.Formation{Relaunches: 2, StoppedAt: at(0)}); got != 3 {
		t.Fatalf("never ready counts on: %d", got)
	}
	if got := nextRelaunches(&v1.Formation{Relaunches: 2, ReadyAt: at(-relaunchStable + time.Second), StoppedAt: at(0)}); got != 3 {
		t.Fatalf("degraded soon after ready counts on: %d", got)
	}
	if got := nextRelaunches(&v1.Formation{Relaunches: 2, ReadyAt: at(-relaunchStable), StoppedAt: at(0)}); got != 1 {
		t.Fatalf("a stretch served starts over: %d", got)
	}
	if nextRelaunches(&v1.Formation{Relaunches: maxRelaunches, StoppedAt: at(0)}) <= maxRelaunches {
		t.Fatal("past the limit the formation stays failed")
	}
	if nextRelaunches(&v1.Formation{Relaunches: maxRelaunches - 1, StoppedAt: at(0)}) > maxRelaunches {
		t.Fatal("the last allowed relaunch still runs")
	}
}

// A seat request is refused as a formation request, its block naming the formation to run instead
func TestRunRefusesASeatRequest(t *testing.T) {
	m := &Manager{}
	run := &v1.RunRequest{Name: "qwen:Q5/stage1", Seat: &v1.SeatSpec{Role: "stage", Name: "qwen:Q5", FormationId: "f1"}}
	_, _, err := m.Run(context.Background(), run)
	if err == nil || !strings.Contains(err.Error(), "qwen:Q5/stage1 is a stage seat of the formation qwen:Q5, not a formation of its own; run qwen:Q5") {
		t.Fatalf("a seat request is refused: %v", err)
	}
}
