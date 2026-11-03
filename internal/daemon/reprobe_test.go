package daemon

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A host probe counting its runs
type countingProbe struct{ runs atomic.Int32 }

func (*countingProbe) ID() string               { return "counting" }
func (*countingProbe) Description() string      { return "counts" }
func (*countingProbe) Runs(string, string) bool { return true }
func (p *countingProbe) Run(context.Context) host.Result {
	p.runs.Add(1)
	return host.Result{Status: v1.ProbeStatus_PROBE_STATUS_OK}
}

// A runtime exit probes the host again at once, past the cached profile, and runs nothing else
func TestInstanceExitReprobesHostOnly(t *testing.T) {
	probe := &countingProbe{}
	prober := host.New([]host.Probe{probe}, nil, time.Hour)
	var published atomic.Int32
	prober.OnProbe = func(*v1.HostProfile) { published.Add(1) }
	if _, err := prober.Profile(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := prober.Profile(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if probe.runs.Load() != 1 {
		t.Fatalf("the cached profile was probed %d times", probe.runs.Load())
	}
	exit := reprobe(prober, slog.New(slog.NewTextHandler(io.Discard, nil)))
	exit()
	exit()
	if probe.runs.Load() != 3 || published.Load() != 3 {
		t.Fatalf("runtime exits probed %d times and published %d profiles, want 3 and 3", probe.runs.Load(), published.Load())
	}
}
