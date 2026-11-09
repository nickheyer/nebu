package instances

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/launch"
	"github.com/nickheyer/nebu/pkg/proc"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/snapshot"
	"github.com/nickheyer/nebu/pkg/store"
)

// The snapshotter a launch on this host gets, refused when the host probe found no criu, or no cuda-checkpoint beside an NVIDIA device
func (m *Manager) snapshotter(profile *v1.HostProfile, placement v1.Placement) (*snapshot.Snapshotter, error) {
	nvidia := placement != v1.Placement_PLACEMENT_HOST && slices.ContainsFunc(profile.GetDevices(), func(d *v1.Device) bool { return d.GetVendor() == "nvidia" })
	tools, err := snapshot.Detect(profile, nvidia)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", runtimes.ErrParam, err)
	}
	return &snapshot.Snapshotter{Tools: tools, Log: m.Log, CUDAPIDs: m.CUDAPIDs}, nil
}

// The image key of a run, the same for every launch of one model, install, params and extra words
func (m *Manager) snapshotKey(p *prepared) string {
	if p.snapshot == nil {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(store.Key(p.stored.GetSourceId(), p.stored.GetRepo(), p.stored.GetGroup())))
	for _, a := range p.stored.GetArtifacts() {
		h.Write([]byte{0})
		h.Write([]byte(a.GetDigest()))
	}
	h.Write([]byte{0})
	h.Write([]byte(p.install.GetId()))
	params := stringParams(p.params)
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range params {
			if !yield(k) {
				return
			}
		}
	}) {
		h.Write([]byte{0})
		h.Write([]byte(k + "=" + params[k]))
	}
	h.Write([]byte{0})
	h.Write([]byte(runtimes.JoinArgs(p.res.args(), p.req.GetArgs())))
	return "snap-" + hex.EncodeToString(h.Sum(nil))[:24]
}

// Where the images of a key live under the instances directory
func (m *Manager) snapshotDir(key string) string {
	return filepath.Join(m.Dir, "snapshots", key)
}

// Restores the image of a key on its recorded port and waits for health, false when no usable image exists so a cold launch follows
func (m *Manager) restoreSnapshot(ctx context.Context, h *tasks.Handle, in *instance, p *prepared, key string) (launch.Handle, bool, error) {
	dir := m.snapshotDir(key)
	meta, ok, err := snapshot.Read(dir)
	if err != nil {
		m.restoreFailed(h, in, dir, true, err)
		return nil, false, nil
	}
	if !ok {
		h.Logf("no snapshot under %s yet, launching cold", key)
		return nil, false, nil
	}
	port, err := m.book(bindHost).Reserve(proc.Range{Low: meta.Port, High: meta.Port}, 1)
	if err != nil {
		m.restoreFailed(h, in, dir, false, fmt.Errorf("port %d the snapshot taken %s listens on is taken now: %w", meta.Port, meta.TakenAt, err))
		return nil, false, nil
	}
	in.mu.Lock()
	in.held = append(in.held, reservation{host: bindHost, port: port})
	in.mu.Unlock()
	h.Message("restoring the snapshot")
	h.Logf("restoring the snapshot taken %s from %s", meta.TakenAt, dir)
	logPath := m.logPath(in.id())
	m.yield(in)
	pid, err := p.snapshot.Restore(ctx, dir, logPath)
	if err != nil {
		m.release(in)
		if cancelled(ctx, err) {
			return nil, false, errStopped
		}
		m.restoreFailed(h, in, dir, true, err)
		return nil, false, nil
	}
	handle, err := launch.Reap(pid, logPath)
	if err != nil {
		proc.Terminate(ctx, pid, in.grace())
		m.release(in)
		m.restoreFailed(h, in, dir, true, fmt.Errorf("restored pid %d could not be followed: %w", pid, err))
		return nil, false, nil
	}
	in.attach(handle)
	gen := in.snapshot().GetGeneration()
	in.update(func(r *v1.Instance) {
		r.Pid = int32(pid)
		r.Endpoint = fmt.Sprintf("http://%s", hostPort(bindHost, meta.Port))
		r.Command, r.Params, r.Inventory, r.InstallId = meta.Command, meta.Params, meta.Inventory, p.install.GetId()
		r.Conditions = SetCondition(r.Conditions, CondLaunched, true, ReasonRunning, pidsText(r), gen)
	})
	if err := m.waitReady(ctx, h, in, handle); err != nil {
		if cancelled(ctx, err) {
			return nil, false, errStopped
		}
		in.stopProcess(handle, watchExit(handle), ReasonLaunchFailed)
		in.recordDrainCrash()
		m.release(in)
		m.restoreFailed(h, in, dir, true, fmt.Errorf("the restored process answered no health: %w", err))
		return nil, false, nil
	}
	m.confirm(in)
	in.update(func(r *v1.Instance) {
		r.Conditions = SetCondition(r.Conditions, CondSnapshot, true, ReasonRestored, "restored from the snapshot taken "+meta.TakenAt, gen)
	})
	h.Logf("restored from the snapshot taken %s, pid %d on port %d", meta.TakenAt, pid, meta.Port)
	return handle, true, nil
}

// Records why the image was not restored on the Snapshotted condition before the cold launch, dropping an image that is unusable
func (m *Manager) restoreFailed(h *tasks.Handle, in *instance, dir string, drop bool, err error) {
	rec := in.snapshot()
	h.Logf("snapshot restore failed, the launch starts cold: %v", err)
	m.Log.Warn("snapshot restore failed", "name", rec.GetName(), "dir", dir, "err", err)
	in.update(func(r *v1.Instance) {
		r.Conditions = SetCondition(r.Conditions, CondSnapshot, false, ReasonRestoreFailed, err.Error(), rec.GetGeneration())
	})
	if drop {
		os.RemoveAll(dir)
	}
}

// Dumps the ready process under the key before it takes traffic, recording the outcome on the Snapshotted condition
func (m *Manager) takeSnapshot(ctx context.Context, h *tasks.Handle, in *instance, p *prepared, handle launch.Handle, key string) error {
	rec := in.snapshot()
	gen := rec.GetGeneration()
	dir := m.snapshotDir(key)
	addr, err := addressOf(rec.GetEndpoint())
	if err != nil {
		return err
	}
	h.Message("snapshotting")
	h.Logf("snapshotting pid %d into %s", handle.Pid(), dir)
	meta, err := p.snapshot.Dump(ctx, snapshot.Request{Pid: handle.Pid(), Dir: dir, Port: addr.Port, LogPath: m.logPath(rec.GetId()), Command: rec.GetCommand(), Params: rec.GetParams(), Inventory: rec.GetInventory()})
	if err != nil {
		h.Logf("snapshot failed: %v", err)
		m.Log.Warn("snapshot failed", "name", rec.GetName(), "err", err)
		in.update(func(r *v1.Instance) {
			r.Conditions = SetCondition(r.Conditions, CondSnapshot, false, ReasonSnapshotFailed, err.Error(), gen)
		})
		return err
	}
	detail := fmt.Sprintf("image at %s taken %s", dir, meta.TakenAt)
	if len(meta.CUDA) > 0 {
		detail += fmt.Sprintf(", CUDA state of pids %s parked and resumed", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(meta.CUDA)), ","), "[]"))
	}
	in.update(func(r *v1.Instance) {
		r.Conditions = SetCondition(r.Conditions, CondSnapshot, true, ReasonSnapshotted, detail, gen)
	})
	h.Logf("snapshotted, %s", detail)
	return nil
}
