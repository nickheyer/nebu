package installs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/build"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	kindBuild   = "build"
	buildLog    = "build.log"
	restartNote = "daemon restarted"
)

// Returned when a build id is not known
var ErrUnknownBuild = errors.New("unknown build")

// Lists recipes with what the host selects for each
func (m *Manager) ListRecipes(ctx context.Context, runtimeID string) ([]*v1.RecipeStatus, error) {
	profile, err := m.Host.Profile(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []*v1.RecipeStatus
	for _, rc := range m.Recipes.List() {
		if runtimeID != "" && rc.Spec.GetRuntimeId() != runtimeID {
			continue
		}
		sel, err := rc.Select(profile, build.Options{Defaults: m.Defaults})
		if err != nil {
			out = append(out, &v1.RecipeStatus{Recipe: rc.Spec, Unmet: []string{err.Error()}})
			continue
		}
		out = append(out, sel.Status())
	}
	return out, nil
}

// Picks the named recipe, else the runtime manifest's recipe
func (m *Manager) recipe(req *v1.BuildRequest) (*build.Recipe, error) {
	if req.GetRecipeId() != "" {
		rc, err := m.Recipes.Get(req.GetRecipeId())
		if err != nil {
			return nil, err
		}
		if req.GetRuntimeId() != "" && rc.Spec.GetRuntimeId() != req.GetRuntimeId() {
			return nil, fmt.Errorf("%w: recipe %s builds %s, not %s", build.ErrSelection, rc.Spec.GetId(), rc.Spec.GetRuntimeId(), req.GetRuntimeId())
		}
		return rc, nil
	}
	if req.GetRuntimeId() == "" {
		return nil, fmt.Errorf("%w: runtime or recipe required", build.ErrSelection)
	}
	rt, err := m.Runtimes.Get(req.GetRuntimeId())
	if err != nil {
		return nil, err
	}
	if id := rt.Manifest.GetAcquire().GetRecipeId(); id != "" {
		return m.Recipes.Get(id)
	}
	candidates := m.Recipes.ForRuntime(req.GetRuntimeId())
	switch len(candidates) {
	case 0:
		return nil, fmt.Errorf("%w: no recipe builds %s", build.ErrUnknownRecipe, req.GetRuntimeId())
	case 1:
		return candidates[0], nil
	}
	var ids []string
	for _, c := range candidates {
		ids = append(ids, c.Spec.GetId())
	}
	return nil, fmt.Errorf("%w: %s has recipes %s, pass one", build.ErrSelection, req.GetRuntimeId(), strings.Join(ids, ", "))
}

// Resolves a request to a build, reusing a finished one
func (m *Manager) Build(ctx context.Context, req *v1.BuildRequest) (*v1.Build, *v1.Task, error) {
	rc, err := m.recipe(req)
	if err != nil {
		return nil, nil, err
	}
	profile, err := m.Host.Profile(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	sel, err := rc.Select(profile, build.Options{
		Variant:  req.GetVariant(),
		Vars:     req.GetVars(),
		Sandbox:  req.GetSandbox(),
		Image:    req.GetImage(),
		Ref:      req.GetRef(),
		Defaults: m.Defaults,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := sel.Err(); err != nil {
		return nil, nil, err
	}
	b := sel.Build()
	if err := m.Engine.Resolve(ctx, sel, b); err != nil {
		return nil, nil, err
	}
	m.buildMu.Lock()
	defer m.buildMu.Unlock()
	if task, ok := m.building[b.GetId()]; ok {
		existing, err := m.DB.GetBuild(ctx, b.GetId())
		if err == nil {
			return existing, task, nil
		}
	}
	if !req.GetForce() {
		if existing, install := m.cached(ctx, b.GetId()); existing != nil {
			task := m.Tasks.Start(kindBuild, "build "+rc.Spec.GetId()+" "+b.GetVariant(), map[string]string{"build": b.GetId(), "runtime": b.GetRuntimeId(), "cached": "true"}, func(ctx context.Context, h *tasks.Handle) error {
				h.Logf("build %s already done, install %s at %s", existing.GetId(), install.GetId(), install.GetPath())
				h.Progress(1, 1, "cached")
				return nil
			})
			return existing, task, nil
		}
	}
	b.State = v1.BuildState_BUILD_STATE_RUNNING
	title := fmt.Sprintf("build %s %s %s", rc.Spec.GetId(), b.GetVariant(), b.GetRef())
	// The task works on its own copy once ids exist
	var job *v1.Build
	ready := make(chan struct{})
	task := m.Tasks.Start(kindBuild, title, map[string]string{"build": b.GetId(), "runtime": b.GetRuntimeId(), "recipe": rc.Spec.GetId()}, func(ctx context.Context, h *tasks.Handle) error {
		<-ready
		err := m.runBuild(ctx, h, sel, job)
		m.buildMu.Lock()
		delete(m.building, job.GetId())
		m.buildMu.Unlock()
		return err
	})
	b.TaskId = task.GetId()
	job = proto.Clone(b).(*v1.Build)
	if m.building == nil {
		m.building = map[string]*v1.Task{}
	}
	m.building[b.GetId()] = task
	// The running row and its event land before the task can write a final state over them
	err = m.DB.PutBuild(ctx, b)
	if err != nil {
		delete(m.building, b.GetId())
		m.Tasks.Cancel(task.GetId())
	} else {
		m.publishBuild(b, v1.EventAction_EVENT_ACTION_CREATED)
	}
	close(ready)
	if err != nil {
		return nil, nil, err
	}
	return proto.Clone(b).(*v1.Build), task, nil
}

// Returns a finished build and install when both still exist
func (m *Manager) cached(ctx context.Context, id string) (*v1.Build, *v1.Install) {
	existing, err := m.DB.GetBuild(ctx, id)
	if err != nil || existing.GetState() != v1.BuildState_BUILD_STATE_SUCCEEDED {
		return nil, nil
	}
	if info, err := os.Stat(existing.GetBinary()); err != nil || info.IsDir() {
		return nil, nil
	}
	install, err := m.DB.GetInstall(ctx, existing.GetInstallId())
	if err != nil {
		return nil, nil
	}
	return existing, install
}

func (m *Manager) runBuild(ctx context.Context, h *tasks.Handle, sel *build.Selection, b *v1.Build) error {
	if err := os.MkdirAll(b.GetDir(), 0o755); err != nil {
		return m.finishBuild(ctx, b, err)
	}
	logFile, err := os.Create(filepath.Join(b.GetDir(), buildLog))
	if err != nil {
		return m.finishBuild(ctx, b, err)
	}
	defer logFile.Close()
	out := &lineWriter{fn: func(line string) { h.Logf("%s", line) }}
	w := io.MultiWriter(logFile, out)
	err = m.Engine.Run(ctx, sel, b, w, func(done, total int, name string) { h.Progress(uint64(done), uint64(total), name) })
	out.flush()
	if err != nil {
		fmt.Fprintf(logFile, "error: %v\n", err)
		return m.finishBuild(ctx, b, err)
	}
	rt, err := m.Runtimes.Get(b.GetRuntimeId())
	if err != nil {
		return m.finishBuild(ctx, b, err)
	}
	in := &v1.Install{
		Id:        installID(b.GetRuntimeId(), b.GetBinary()),
		RuntimeId: b.GetRuntimeId(),
		Kind:      v1.InstallKind_INSTALL_KIND_BUILT,
		Path:      b.GetBinary(),
		Dir:       filepath.Dir(b.GetBinary()),
		Version:   b.GetRef(),
		Origin:    "recipe " + b.GetRecipeId() + " " + b.GetVariant(),
		BuildId:   b.GetId(),
		CreatedAt: timestamppb.Now(),
	}
	if in.Version == "" {
		in.Version = b.GetCommit()
	}
	m.probe(ctx, rt, in)
	in.Facts["recipe"] = b.GetRecipeId()
	in.Facts["variant"] = b.GetVariant()
	in.Facts["build"] = b.GetId()
	if b.GetCommit() != "" {
		in.Facts["commit"] = b.GetCommit()
	}
	if err := m.DB.PutInstall(ctx, in); err != nil {
		return m.finishBuild(ctx, b, err)
	}
	m.publishInstall(in, v1.EventAction_EVENT_ACTION_CREATED)
	b.InstallId = in.GetId()
	h.Logf("installed %s at %s", in.GetId(), in.GetPath())
	return m.finishBuild(ctx, b, nil)
}

func (m *Manager) finishBuild(ctx context.Context, b *v1.Build, err error) error {
	b.FinishedAt = timestamppb.Now()
	if err != nil {
		b.State = v1.BuildState_BUILD_STATE_FAILED
		b.Error = err.Error()
	} else {
		b.State = v1.BuildState_BUILD_STATE_SUCCEEDED
	}
	if perr := m.DB.PutBuild(context.Background(), b); perr != nil {
		m.Log.Warn("build record write failed", "id", b.GetId(), "err", perr)
	}
	m.publishBuild(b, v1.EventAction_EVENT_ACTION_UPDATED)
	return err
}

// Lists builds newest first
func (m *Manager) ListBuilds(ctx context.Context, runtimeID string) ([]*v1.Build, error) {
	return m.DB.ListBuilds(ctx, runtimeID)
}

// Returns one build by id
func (m *Manager) GetBuild(ctx context.Context, id string) (*v1.Build, error) {
	b, err := m.DB.GetBuild(ctx, id)
	if db.IsNotFound(err) {
		return nil, fmt.Errorf("%w %q", ErrUnknownBuild, id)
	}
	return b, err
}

// Returns the transcript of a build
func (m *Manager) BuildLog(ctx context.Context, id string) ([]string, error) {
	b, err := m.GetBuild(ctx, id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(b.GetDir(), buildLog))
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n"), nil
}

// Removes a build, its tree, and the install it produced
func (m *Manager) RemoveBuild(ctx context.Context, id string) (*v1.Build, error) {
	b, err := m.GetBuild(ctx, id)
	if err != nil {
		return nil, err
	}
	m.buildMu.Lock()
	_, running := m.building[id]
	m.buildMu.Unlock()
	if running {
		return nil, fmt.Errorf("build %s is still running, cancel its task first", id)
	}
	if b.GetInstallId() != "" {
		if in, err := m.DB.GetInstall(ctx, b.GetInstallId()); err == nil {
			if _, err := m.DB.DeleteInstall(ctx, in.GetId()); err != nil {
				return nil, err
			}
			m.publishInstall(in, v1.EventAction_EVENT_ACTION_DELETED)
		}
	}
	if _, err := m.DB.DeleteBuild(ctx, id); err != nil {
		return nil, err
	}
	if b.GetDir() != "" && strings.HasPrefix(b.GetDir(), m.Engine.Root) {
		if err := os.RemoveAll(b.GetDir()); err != nil {
			return nil, err
		}
	}
	m.publishBuild(b, v1.EventAction_EVENT_ACTION_DELETED)
	return b, nil
}

// Marks builds a previous daemon left running as failed
func (m *Manager) RecoverBuilds(ctx context.Context) error {
	n, err := m.DB.FailUnfinishedBuilds(ctx, restartNote, time.Now())
	if err != nil {
		return err
	}
	if n > 0 {
		m.Log.Info("marked unfinished builds from the previous daemon as failed", "builds", n)
	}
	return nil
}

// Splits a stream into lines for the task log
type lineWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	fn   func(string)
	last time.Time
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		data := w.buf.Bytes()
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(data[:i]), "\r")
		w.buf.Next(i + 1)
		if strings.TrimSpace(line) != "" {
			w.fn(line)
		}
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		w.fn(strings.TrimRight(w.buf.String(), "\r\n"))
		w.buf.Reset()
	}
}
