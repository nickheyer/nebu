package formations

import (
	"context"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

// A plan made by hand, for tests that launch without the formation planner
type Planned struct {
	Plan   *v1.FormationPlan
	Stored *v1.StoredModel
	Rt     runtimes.Runtime
	Name   string
}

func (p Planned) inner() *planned {
	return &planned{plan: p.Plan, descriptor: p.Stored.GetDescriptor_(), stored: p.Stored, rt: p.Rt, name: p.Name}
}

// Records a formation from a plan made by hand and starts its launch task
func (m *Manager) StartPlanned(ctx context.Context, run *v1.RunRequest, p Planned) (*v1.Formation, *v1.Task, error) {
	return m.start(ctx, run, p.inner(), nil)
}

// Installs the planner relaunches go through
func (m *Manager) SetPlanner(fn func(ctx context.Context, run *v1.RunRequest) (Planned, error)) {
	m.planner = func(ctx context.Context, run *v1.RunRequest) (*planned, error) {
		p, err := fn(ctx, run)
		if err != nil {
			return nil, err
		}
		return p.inner(), nil
	}
}

// Sets how long a degraded formation waits before it launches again, returning the restore
func SetRelaunchDelay(d time.Duration) func() {
	was := relaunchDelay
	relaunchDelay = d
	return func() { relaunchDelay = was }
}

// The key stages keep a model's tensor cache under
func CacheKeyOf(s *v1.StoredModel) string { return cacheKeyOf(s) }

// Sums seat measurements by node
func NodeSums(seats []*v1.Seat) []*v1.NodeMeasurements { return nodeSums(seats) }

// Bytes the head's log says it placed on an rpc backend
func HeadStreamed(lines []string, address string) (uint64, bool) { return headStreamed(lines, address) }

// Launches a formation again the way a failure or a restart does
func (m *Manager) Relaunch(ctx context.Context, prev *v1.Formation) (*v1.Formation, *v1.Task, error) {
	return m.relaunch(ctx, prev)
}
