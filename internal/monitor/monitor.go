// Package monitor watches sources for new revisions and groups.
package monitor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/slots"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	kindCheck   = "check"
	findingsMax = 500
)

var (
	// Returned when a watch id is not known
	ErrUnknownWatch = errors.New("unknown watch")
	// Returned when a finding id is not known
	ErrUnknownFinding = errors.New("unknown finding")
	// Returned when a watch request is malformed
	ErrWatch = errors.New("invalid watch")
)

// Keeps watches, checks them on schedule, pulls and swaps
type Manager struct {
	DB        *db.DB
	Inspector *inspect.Inspector
	Puller    *pull.Puller
	Slots     *slots.Manager
	Tasks     *tasks.Manager
	Events    *events.Bus
	Interval  time.Duration
	Disabled  bool
	Log       *slog.Logger

	mu       sync.Mutex
	watches  map[string]*v1.Watch
	checking bool
}

// Loads every watch from the store
func (m *Manager) Load(ctx context.Context) error {
	list, err := m.DB.ListWatches(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.watches = map[string]*v1.Watch{}
	for _, w := range list {
		m.watches[w.GetId()] = w
	}
	m.mu.Unlock()
	return nil
}

// Checks every watch on the interval until ctx ends
func (m *Manager) Run(ctx context.Context) {
	if m.Disabled || m.Interval <= 0 {
		return
	}
	ticker := time.NewTicker(m.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if len(m.List()) == 0 {
				continue
			}
			if _, err := m.Check(ctx, ""); err != nil {
				m.Log.Warn("scheduled check failed to start", "err", err)
			}
		}
	}
}

// Lists watches oldest first
func (m *Manager) List() []*v1.Watch {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*v1.Watch, 0, len(m.watches))
	for _, w := range m.watches {
		out = append(out, proto.Clone(w).(*v1.Watch))
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].GetCreatedAt().AsTime(), out[j].GetCreatedAt().AsTime()
		if a.Equal(b) {
			return out[i].GetId() < out[j].GetId()
		}
		return a.Before(b)
	})
	return out
}

func (m *Manager) find(id string) (*v1.Watch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w, ok := m.watches[id]; ok {
		return proto.Clone(w).(*v1.Watch), nil
	}
	for _, w := range m.watches {
		if w.GetRepo() == id {
			return proto.Clone(w).(*v1.Watch), nil
		}
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownWatch, id)
}

func (m *Manager) save(w *v1.Watch, action v1.EventAction) {
	m.mu.Lock()
	if m.watches == nil {
		m.watches = map[string]*v1.Watch{}
	}
	m.watches[w.GetId()] = proto.Clone(w).(*v1.Watch)
	m.mu.Unlock()
	if err := m.DB.PutWatch(context.Background(), w); err != nil {
		m.Log.Warn("watch record write failed", "id", w.GetId(), "err", err)
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_WATCH, action, w.GetId(), &v1.Event_Watch{Watch: w})
}

// Adds a watch and records its baseline with one check
func (m *Manager) Add(ctx context.Context, req *v1.AddWatchRequest) (*v1.Watch, error) {
	if strings.TrimSpace(req.GetRepo()) == "" {
		return nil, fmt.Errorf("%w: repo required", ErrWatch)
	}
	if req.GetGroupMatch() != "" {
		if _, err := regexp.Compile(req.GetGroupMatch()); err != nil {
			return nil, fmt.Errorf("%w: group match: %v", ErrWatch, err)
		}
	}
	src, err := m.Inspector.Sources.Get(req.GetSourceId())
	if err != nil {
		return nil, err
	}
	if req.GetSlotId() != "" {
		if _, _, err := m.Slots.Get(req.GetSlotId()); err != nil {
			return nil, err
		}
	}
	for _, w := range m.List() {
		if w.GetSourceId() == src.Spec().GetId() && w.GetRepo() == req.GetRepo() && w.GetRevision() == req.GetRevision() {
			return nil, fmt.Errorf("%w: %s is already watched as %s", ErrWatch, req.GetRepo(), w.GetId())
		}
	}
	w := &v1.Watch{
		Id:         newID(),
		SourceId:   src.Spec().GetId(),
		Repo:       req.GetRepo(),
		Revision:   req.GetRevision(),
		GroupMatch: req.GetGroupMatch(),
		AutoPull:   req.GetAutoPull(),
		SlotId:     req.GetSlotId(),
		RuntimeId:  req.GetRuntimeId(),
		Params:     req.GetParams(),
		CreatedAt:  timestamppb.Now(),
	}
	if _, err := m.check(ctx, nil, w); err != nil {
		return nil, err
	}
	m.save(w, v1.EventAction_EVENT_ACTION_CREATED)
	return w, nil
}

// Removes a watch and its findings
func (m *Manager) Remove(ctx context.Context, id string) (*v1.Watch, error) {
	w, err := m.find(id)
	if err != nil {
		return nil, err
	}
	if _, err := m.DB.DeleteWatch(ctx, w.GetId()); err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.watches, w.GetId())
	m.mu.Unlock()
	m.Events.Publish(v1.EventKind_EVENT_KIND_WATCH, v1.EventAction_EVENT_ACTION_DELETED, w.GetId(), &v1.Event_Watch{Watch: w})
	return w, nil
}

// Starts a task checking one watch, or all when empty
func (m *Manager) Check(ctx context.Context, id string) (*v1.Task, error) {
	var targets []*v1.Watch
	if id == "" {
		targets = m.List()
	} else {
		w, err := m.find(id)
		if err != nil {
			return nil, err
		}
		targets = []*v1.Watch{w}
	}
	m.mu.Lock()
	if m.checking && id == "" {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: a check is already running", ErrWatch)
	}
	m.checking = id == ""
	m.mu.Unlock()
	title := fmt.Sprintf("check %d watches", len(targets))
	if len(targets) == 1 {
		title = "check " + targets[0].GetRepo()
	}
	return m.Tasks.Start(kindCheck, title, map[string]string{"watch": id}, func(ctx context.Context, h *tasks.Handle) error {
		defer func() {
			if id == "" {
				m.mu.Lock()
				m.checking = false
				m.mu.Unlock()
			}
		}()
		var failed []string
		for i, w := range targets {
			h.Progress(uint64(i), uint64(len(targets)), "checking "+w.GetRepo())
			if _, err := m.check(ctx, h, w); err != nil {
				failed = append(failed, w.GetRepo()+": "+err.Error())
			}
		}
		h.Progress(uint64(len(targets)), uint64(len(targets)), "done")
		if len(failed) > 0 {
			return errors.New(strings.Join(failed, "; "))
		}
		return nil
	}), nil
}

// Resolves a watch, records changes, and runs automatic actions
func (m *Manager) check(ctx context.Context, h *tasks.Handle, w *v1.Watch) ([]*v1.Finding, error) {
	logf := func(format string, args ...any) {
		if h != nil {
			h.Logf(format, args...)
		}
	}
	_, model, err := m.Inspector.ResolveFresh(ctx, w.GetSourceId(), w.GetRepo(), w.GetRevision())
	if err != nil {
		w.Error = err.Error()
		w.CheckedAt = timestamppb.Now()
		if w.GetId() != "" {
			m.save(w, v1.EventAction_EVENT_ACTION_UPDATED)
		}
		return nil, err
	}
	var groups []string
	for _, g := range formats.Groups(model) {
		groups = append(groups, g.Name)
	}
	sort.Strings(groups)
	baseline := w.GetLastCommit() == "" && len(w.GetKnownGroups()) == 0
	known := map[string]bool{}
	for _, g := range w.GetKnownGroups() {
		known[g] = true
	}
	current := map[string]bool{}
	for _, g := range groups {
		current[g] = true
	}
	var findings []*v1.Finding
	newFinding := func(kind v1.FindingKind, group, detail string) *v1.Finding {
		return &v1.Finding{Id: newID(), WatchId: w.GetId(), Kind: kind, Repo: w.GetRepo(), Commit: model.GetCommit(), Group: group, Detail: detail, FoundAt: timestamppb.Now()}
	}
	if !baseline {
		if model.GetCommit() != "" && model.GetCommit() != w.GetLastCommit() {
			findings = append(findings, newFinding(v1.FindingKind_FINDING_KIND_NEW_REVISION, "", fmt.Sprintf("commit %s replaced %s", short(model.GetCommit()), short(w.GetLastCommit()))))
		}
		for _, g := range groups {
			if !known[g] {
				findings = append(findings, newFinding(v1.FindingKind_FINDING_KIND_NEW_GROUP, g, "new weight group "+g))
			}
		}
		for _, g := range w.GetKnownGroups() {
			if !current[g] {
				findings = append(findings, newFinding(v1.FindingKind_FINDING_KIND_REMOVED_GROUP, g, "weight group "+g+" is gone"))
			}
		}
	}
	w.LastCommit, w.KnownGroups, w.CheckedAt, w.Error = model.GetCommit(), groups, timestamppb.Now(), ""
	if w.GetId() != "" {
		m.save(w, v1.EventAction_EVENT_ACTION_UPDATED)
	}
	for _, f := range findings {
		if err := m.DB.PutFinding(ctx, f); err != nil {
			m.Log.Warn("finding write failed", "err", err)
		}
		m.Events.Publish(v1.EventKind_EVENT_KIND_FINDING, v1.EventAction_EVENT_ACTION_CREATED, f.GetId(), &v1.Event_Finding{Finding: f})
		logf("%s: %s", w.GetRepo(), f.GetDetail())
	}
	if baseline {
		logf("%s: baseline commit %s with %d groups", w.GetRepo(), short(model.GetCommit()), len(groups))
	} else if len(findings) == 0 {
		logf("%s: unchanged at %s", w.GetRepo(), short(model.GetCommit()))
	}
	if w.GetAutoPull() && len(findings) > 0 {
		m.act(ctx, logf, w, model, findings)
	}
	m.DB.PruneFindings(ctx, findingsMax)
	return findings, nil
}

// Pulls groups the findings touch, then swaps the slot
func (m *Manager) act(ctx context.Context, logf func(string, ...any), w *v1.Watch, model *v1.Model, findings []*v1.Finding) {
	var re *regexp.Regexp
	if w.GetGroupMatch() != "" {
		re, _ = regexp.Compile(w.GetGroupMatch())
	}
	wanted := map[string]*v1.Finding{}
	for _, f := range findings {
		switch f.GetKind() {
		case v1.FindingKind_FINDING_KIND_NEW_GROUP:
			if re == nil || re.MatchString(f.GetGroup()) {
				wanted[f.GetGroup()] = f
			}
		case v1.FindingKind_FINDING_KIND_NEW_REVISION:
			for _, g := range w.GetKnownGroups() {
				if _, err := m.Puller.Store.ReadManifest(w.GetSourceId(), w.GetRepo(), g); err == nil && (re == nil || re.MatchString(g)) {
					wanted[g] = f
				}
			}
		}
	}
	targets := make([]string, 0, len(wanted))
	for g := range wanted {
		targets = append(targets, g)
	}
	sort.Strings(targets)
	if len(targets) == 0 {
		return
	}
	served := ""
	if w.GetSlotId() != "" {
		if s, _, err := m.Slots.Get(w.GetSlotId()); err == nil && s.GetRequest().GetRepo() == w.GetRepo() {
			served = s.GetRequest().GetGroup()
		}
	}
	pulled := []string{}
	for _, g := range targets {
		task, err := m.Puller.Pull(ctx, &v1.PullRequest{SourceId: w.GetSourceId(), Repo: w.GetRepo(), Revision: w.GetRevision(), Group: g})
		if err != nil {
			logf("pull %s %s failed to start: %v", w.GetRepo(), g, err)
			continue
		}
		f := wanted[g]
		f.TaskId = task.GetId()
		m.DB.PutFinding(ctx, f)
		m.Events.Publish(v1.EventKind_EVENT_KIND_FINDING, v1.EventAction_EVENT_ACTION_UPDATED, f.GetId(), &v1.Event_Finding{Finding: f})
		logf("pulling %s %s as task %s", w.GetRepo(), g, task.GetId())
		final, err := m.Tasks.Wait(ctx, task.GetId())
		if err != nil || final.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
			logf("pull %s %s did not succeed: %s", w.GetRepo(), g, final.GetError())
			continue
		}
		pulled = append(pulled, g)
	}
	if w.GetSlotId() == "" || len(pulled) == 0 {
		return
	}
	group := pulled[0]
	for _, g := range pulled {
		if g == served {
			group = g
		}
	}
	run := &v1.RunRequest{SourceId: w.GetSourceId(), Repo: w.GetRepo(), Group: group, RuntimeId: w.GetRuntimeId(), Params: w.GetParams(), SlotId: w.GetSlotId()}
	_, _, task, err := m.Slots.Swap(ctx, &v1.SwapRequest{SlotId: w.GetSlotId(), Run: run})
	if err != nil {
		logf("swap into slot %s failed: %v", w.GetSlotId(), err)
		return
	}
	logf("swapping slot %s to %s %s as task %s", w.GetSlotId(), w.GetRepo(), group, task.GetId())
	if final, err := m.Tasks.Wait(ctx, task.GetId()); err == nil && final.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		logf("swap did not succeed: %s", final.GetError())
	}
}

// Lists findings newest first
func (m *Manager) Findings(ctx context.Context, watchID string, unackedOnly bool) ([]*v1.Finding, error) {
	if watchID != "" {
		w, err := m.find(watchID)
		if err != nil {
			return nil, err
		}
		watchID = w.GetId()
	}
	return m.DB.ListFindings(ctx, watchID, unackedOnly)
}

// Marks a finding as seen
func (m *Manager) Ack(ctx context.Context, id string) (*v1.Finding, error) {
	f, err := m.DB.GetFinding(ctx, id)
	if db.IsNotFound(err) {
		return nil, fmt.Errorf("%w %q", ErrUnknownFinding, id)
	}
	if err != nil {
		return nil, err
	}
	f.Acknowledged = true
	if err := m.DB.PutFinding(ctx, f); err != nil {
		return nil, err
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_FINDING, v1.EventAction_EVENT_ACTION_UPDATED, f.GetId(), &v1.Event_Finding{Finding: f})
	return f, nil
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	if commit == "" {
		return "none"
	}
	return commit
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().String()))[:12]
	}
	return hex.EncodeToString(b[:])
}
