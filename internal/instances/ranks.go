package instances

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/launch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/text"
)

// One data parallel rank of a launch as its record names it
type rankRef struct {
	rank     int
	endpoint string
	pid      int32
	command  []string
	// The mesh port other nodes reach a seat's rank at
	port    uint32
	healthy bool
}

// Every rank a record names, rank 0 alone at the instance endpoint for a launch of one rank
func rankRefs(rec *v1.Instance) []rankRef {
	if len(rec.GetRanks()) == 0 {
		return []rankRef{{endpoint: rec.GetEndpoint(), pid: rec.GetPid(), command: rec.GetCommand(), port: rec.GetSeat().GetPort(), healthy: rec.GetHealthy()}}
	}
	out := make([]rankRef, 0, len(rec.GetRanks()))
	for _, r := range rec.GetRanks() {
		out = append(out, rankRef{rank: int(r.GetRank()), endpoint: r.GetEndpoint(), pid: r.GetPid(), command: r.GetCommand(), port: r.GetPort(), healthy: r.GetHealthy()})
	}
	return out
}

// The rank an endpoint is published under, a replica seat's ranks numbered after every rank of the replicas before it
func PublishedRank(seat *v1.SeatSpec, ranks, rank int) uint32 {
	if seat.GetShape() == v1.Shape_SHAPE_REPLICAS {
		return seat.GetRank()*uint32(max(ranks, 1)) + uint32(rank)
	}
	return uint32(rank)
}

// Where a rank writes its output, rank 0 in the instance log and every other rank beside it
func (m *Manager) rankLogPath(id string, rank int) string {
	if rank == 0 {
		return m.logPath(id)
	}
	return filepath.Join(m.Dir, id+".rank"+strconv.Itoa(rank)+".log")
}

// The saved output files of every rank past rank 0, in rank order
func (m *Manager) rankLogs(id string) []string {
	var out []string
	for rank := 1; ; rank++ {
		path := m.rankLogPath(id, rank)
		if _, err := os.Stat(path); err != nil {
			return out
		}
		out = append(out, path)
	}
}

// Renders every rank of a launch in rank order, each through render and then extra on its own launch inputs
func renderRanks(in runtimes.Launch, ranks int, render func(runtimes.Launch) (*runtimes.Command, error), extra func(runtimes.Launch, *runtimes.Command) error) ([]*runtimes.Command, error) {
	out := make([]*runtimes.Command, 0, ranks)
	for rank := range ranks {
		in.Rank = rank
		cmd, err := render(in)
		if err != nil {
			return nil, err
		}
		if err := extra(in, cmd); err != nil {
			return nil, err
		}
		if cmd.Port == 0 {
			cmd.Port = in.Port
		}
		out = append(out, cmd)
	}
	return out, nil
}

// Records of every rank of a launch of several, each with its endpoint on host and its mesh port
func rankRecords(cmds []*runtimes.Command, host string, mesh []uint32) []*v1.InstanceRank {
	if len(cmds) < 2 {
		return nil
	}
	out := make([]*v1.InstanceRank, len(cmds))
	for i, cmd := range cmds {
		out[i] = &v1.InstanceRank{
			Rank:     uint32(i),
			Endpoint: fmt.Sprintf("http://%s", hostPort(host, cmd.Port)),
			Command:  append([]string{cmd.Command}, cmd.Args...),
		}
		if i < len(mesh) {
			out[i].Port = mesh[i]
		}
	}
	return out
}

// Joins a host and port, bracketing an IPv6 host
func hostPort(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return host + ":" + strconv.Itoa(port)
}

// Starts every rank's command, ranks past one supervised as one group, stopping the started ranks when one fails to start
func (m *Manager) startRanks(ctx context.Context, h *tasks.Handle, in *instance, cmds []*runtimes.Command, dir string) (launch.Handle, error) {
	rec := in.snapshot()
	refs := rankRefs(rec)
	if len(cmds) == 1 {
		h.Logf("command: %s", commandLine(rec.GetCommand()))
	}
	h.Progress(0, 0, "launching")
	if stale := m.rankLogs(rec.GetId()); len(stale) >= len(cmds) {
		for _, path := range stale[len(cmds)-1:] {
			os.Remove(path)
		}
	}
	var handles []launch.Handle
	for i, cmd := range cmds {
		if len(cmds) > 1 {
			h.Logf("rank %d command: %s", i, commandLine(refs[i].command))
		}
		handle, err := m.Launcher.Launch(ctx, launch.Spec{Command: cmd.Command, Args: cmd.Args, Env: cmd.Env, Dir: dir, LogPath: m.rankLogPath(rec.GetId(), i)})
		if err != nil {
			stopStarted(handles, in.grace())
			if len(cmds) > 1 {
				return nil, fmt.Errorf("rank %d: %w", i, err)
			}
			return nil, err
		}
		handles = append(handles, handle)
	}
	handle := handles[0]
	if len(handles) > 1 {
		handle = launch.NewGroup(handles, in.grace())
	}
	in.attach(handle)
	gen := rec.GetGeneration()
	in.update(func(r *v1.Instance) {
		r.Pid = int32(handles[0].Pid())
		for i, rr := range r.GetRanks() {
			rr.Pid = int32(handles[i].Pid())
		}
		r.Conditions = SetCondition(r.Conditions, CondLaunched, true, ReasonRunning, pidsText(r), gen)
	})
	return handle, nil
}

// Stops ranks already started when a later rank could not start
func stopStarted(handles []launch.Handle, grace time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), grace+stopSlack)
	defer cancel()
	for _, h := range handles {
		h.Stop(ctx, grace)
	}
}

// The process id of a launch, or of every rank in rank order
func pidsText(r *v1.Instance) string {
	if len(r.GetRanks()) == 0 {
		return fmt.Sprintf("pid %d", r.GetPid())
	}
	pids := make([]string, len(r.GetRanks()))
	for i, rr := range r.GetRanks() {
		pids[i] = strconv.Itoa(int(rr.GetPid()))
	}
	return "pids " + strings.Join(pids, ", ")
}

// The endpoints of every rank, rank 0 first
func endpointsText(r *v1.Instance) string {
	refs := rankRefs(r)
	out := make([]string, len(refs))
	for i, ref := range refs {
		out[i] = ref.endpoint
	}
	return strings.Join(out, ", ")
}

// Loads and unloads an adapter on every rank of a launch and lists rank 0's
type rankAdapters []runtimes.AdapterClient

// Adapters rank 0 applies, every rank holding the same
func (a rankAdapters) List(ctx context.Context) ([]runtimes.Adapter, error) { return a[0].List(ctx) }

// Loads on every rank, unloading from the ranks before when one rank refuses
func (a rankAdapters) Load(ctx context.Context, name, path string) error {
	for i, c := range a {
		if err := c.Load(ctx, name, path); err != nil {
			errs := []error{fmt.Errorf("rank %d: %w", i, err)}
			for j := range i {
				if uerr := a[j].Unload(ctx, name); uerr != nil {
					errs = append(errs, fmt.Errorf("unloading from rank %d again: %w", j, uerr))
				}
			}
			return errors.Join(errs...)
		}
	}
	return nil
}

// Unloads from every rank, naming each rank that refused
func (a rankAdapters) Unload(ctx context.Context, name string) error {
	var errs []error
	for i, c := range a {
		if err := c.Unload(ctx, name); err != nil {
			errs = append(errs, fmt.Errorf("rank %d: %w", i, err))
		}
	}
	return errors.Join(errs...)
}

// The adapter client of every rank as one, nil when the launch switches no adapters
// Whether the launch switches adapters, never for a record whose runtime is gone
func switchesAdapters(rt runtimes.Runtime, rec *v1.Instance) bool {
	return rt != nil && adaptersOf(rt, rec) != nil
}

func adaptersOf(rt runtimes.Runtime, rec *v1.Instance) runtimes.AdapterClient {
	refs := rankRefs(rec)
	if len(refs) == 1 {
		return rt.Adapters(refs[0].endpoint, rec.GetParams())
	}
	var out rankAdapters
	for _, ref := range refs {
		c := rt.Adapters(ref.endpoint, rec.GetParams())
		if c == nil {
			return nil
		}
		out = append(out, c)
	}
	return out
}

// Puts every rank of a launch to sleep and wakes every rank
type rankSleeper []runtimes.SleepClient

// Sleeps every rank, waking the ranks before when one rank will not sleep
func (s rankSleeper) Sleep(ctx context.Context) error {
	for i, c := range s {
		if err := c.Sleep(ctx); err != nil {
			errs := []error{fmt.Errorf("rank %d: %w", i, err)}
			for j := range i {
				if werr := s[j].Wake(ctx); werr != nil {
					errs = append(errs, fmt.Errorf("waking rank %d again: %w", j, werr))
				}
			}
			return errors.Join(errs...)
		}
	}
	return nil
}

// Wakes every rank, naming each rank that did not wake
func (s rankSleeper) Wake(ctx context.Context) error {
	var errs []error
	for i, c := range s {
		if err := c.Wake(ctx); err != nil {
			errs = append(errs, fmt.Errorf("rank %d: %w", i, err))
		}
	}
	return errors.Join(errs...)
}

// The sleep client of every rank as one, nil when the launch cannot sleep
func sleeperOf(rt runtimes.Runtime, rec *v1.Instance) runtimes.SleepClient {
	refs := rankRefs(rec)
	if len(refs) == 1 {
		return rt.Sleeper(refs[0].endpoint, rec.GetParams())
	}
	var out rankSleeper
	for _, ref := range refs {
		c := rt.Sleeper(ref.endpoint, rec.GetParams())
		if c == nil {
			return nil
		}
		out = append(out, c)
	}
	return out
}

// The port of a rank's endpoint URL
func endpointPort(endpoint string) (int, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Port())
}

// Refuses ranks for a seat whose role is not known by HTTP health, ranks serving requests on HTTP alone
func ranksServeHTTP(rt runtimes.Runtime, seat *v1.SeatSpec, ranks int) error {
	if ranks < 2 || seat == nil {
		return nil
	}
	health, err := runtimes.SeatHealth(rt, seat)
	if err != nil {
		return err
	}
	if health.Kind != runtimes.HealthHTTP {
		return fmt.Errorf("%w: a %s seat answers no HTTP health and runs %d data parallel ranks, which answer requests on HTTP", runtimes.ErrParam, text.Enum(seat.GetShape()), ranks)
	}
	return nil
}
