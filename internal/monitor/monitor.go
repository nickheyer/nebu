// Package monitor watches sources for new revisions and groups, and searches for wanted models.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
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
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	kindCheck   = "check"
	findingsMax = 500
	// Hits a want resolves per check, the search order being the source's own
	wantHits = 10
)

var (
	// Returned when a watch id is not known
	ErrUnknownWatch = errors.New("unknown watch")
	// Returned when a want id is not known
	ErrUnknownWant = errors.New("unknown want")
	// Returned when a finding id is not known
	ErrUnknownFinding = errors.New("unknown finding")
	// Returned when a watch or want request is malformed
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
	wants    map[string]*v1.Want
	checking bool
	// Watches and wants a check holds right now, so two checks never pull and swap one twice
	busy map[string]bool
}

// A log line sink, a task's or nothing
type logf func(format string, args ...any)

// Loads every watch and want from the store
func (m *Manager) Load(ctx context.Context) error {
	list, err := m.DB.ListWatches(ctx)
	if err != nil {
		return err
	}
	wants, err := m.DB.ListWants(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.watches = map[string]*v1.Watch{}
	for _, w := range list {
		m.watches[w.GetId()] = w
	}
	m.wants = map[string]*v1.Want{}
	for _, w := range wants {
		m.wants[w.GetId()] = w
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
			if len(m.List()) == 0 && len(m.openWants()) == 0 {
				continue
			}
			if _, err := m.Check(ctx, "", false); err != nil {
				m.Log.Warn("scheduled check failed to start", "err", err)
			}
		}
	}
}

// Clones rows oldest first, ids breaking ties
func oldestFirst[T proto.Message](rows map[string]T, created func(T) *timestamppb.Timestamp, id func(T) string) []T {
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		out = append(out, proto.Clone(r).(T))
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := created(out[i]).AsTime(), created(out[j]).AsTime()
		if a.Equal(b) {
			return id(out[i]) < id(out[j])
		}
		return a.Before(b)
	})
	return out
}

// Lists watches oldest first
func (m *Manager) List() []*v1.Watch {
	m.mu.Lock()
	defer m.mu.Unlock()
	return oldestFirst(m.watches, (*v1.Watch).GetCreatedAt, (*v1.Watch).GetId)
}

// Lists wants oldest first
func (m *Manager) ListWants() []*v1.Want {
	m.mu.Lock()
	defer m.mu.Unlock()
	return oldestFirst(m.wants, (*v1.Want).GetCreatedAt, (*v1.Want).GetId)
}

// Wants still looking
func (m *Manager) openWants() []*v1.Want {
	var out []*v1.Want
	for _, w := range m.ListWants() {
		if !w.GetSatisfied() {
			out = append(out, w)
		}
	}
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

func (m *Manager) findWant(id string) (*v1.Want, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w, ok := m.wants[id]; ok {
		return proto.Clone(w).(*v1.Want), nil
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownWant, id)
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
	m.Events.Publish(v1.EventKind_EVENT_KIND_WATCH, action, w.GetId(), w)
}

func (m *Manager) saveWant(w *v1.Want, action v1.EventAction) {
	m.mu.Lock()
	if m.wants == nil {
		m.wants = map[string]*v1.Want{}
	}
	m.wants[w.GetId()] = proto.Clone(w).(*v1.Want)
	m.mu.Unlock()
	if err := m.DB.PutWant(context.Background(), w); err != nil {
		m.Log.Warn("want record write failed", "id", w.GetId(), "err", err)
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_WANT, action, w.GetId(), w)
}

// Writes a finding and tells the stream
func (m *Manager) saveFinding(ctx context.Context, f *v1.Finding, action v1.EventAction) {
	if err := m.DB.PutFinding(ctx, f); err != nil {
		m.Log.Warn("finding write failed", "err", err)
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_FINDING, action, f.GetId(), f)
}

// Tells the stream findings went with their watch or want
func (m *Manager) dropFindings(findings []*v1.Finding) {
	for _, f := range findings {
		m.Events.Publish(v1.EventKind_EVENT_KIND_FINDING, v1.EventAction_EVENT_ACTION_DELETED, f.GetId(), f)
	}
}

// Checks what a watch or want would act on: its group regex, slot, runtime, and profile, the profile's id returned
func (m *Manager) target(groupMatch, slotID, runtimeID, profileRef string) (string, error) {
	if groupMatch != "" {
		if _, err := regexp.Compile(groupMatch); err != nil {
			return "", fmt.Errorf("%w: group match: %v", ErrWatch, err)
		}
	}
	if slotID != "" {
		s, _, err := m.Slots.Get(slotID)
		if err != nil {
			return "", err
		}
		if runtimeID == "" {
			runtimeID = s.GetRuntimeId()
		}
	}
	if runtimeID != "" {
		if _, err := m.Inspector.Runtimes.Get(runtimeID); err != nil {
			return "", err
		}
	}
	if profileRef == "" {
		return "", nil
	}
	// The slot's runtime stands in so the row keeps the id a rename cannot strand
	p, err := m.Inspector.Profiles.Resolve(runtimeID, profileRef)
	if err != nil {
		return "", err
	}
	return p.GetId(), nil
}

// Adds a want and looks for it once
func (m *Manager) AddWant(ctx context.Context, req *v1.AddWantRequest) (*v1.Want, error) {
	if strings.TrimSpace(req.GetQuery()) == "" {
		return nil, fmt.Errorf("%w: query required", ErrWatch)
	}
	if req.GetSourceId() != "" {
		if _, err := m.Inspector.Sources.Get(req.GetSourceId()); err != nil {
			return nil, err
		}
	} else if req.GetKind() != v1.SourceKind_SOURCE_KIND_UNSPECIFIED && len(m.Inspector.Sources.OfKind(req.GetKind())) == 0 {
		return nil, fmt.Errorf("%w: no source of kind %s", ErrWatch, req.GetKind())
	}
	if req.GetFormatId() != "" && m.Inspector.Classifier.Spec(req.GetFormatId()) == nil {
		return nil, fmt.Errorf("%w: unknown format %q", ErrWatch, req.GetFormatId())
	}
	profileID, err := m.target(req.GetGroupMatch(), req.GetSlotId(), req.GetRuntimeId(), req.GetProfileId())
	if err != nil {
		return nil, err
	}
	w := &v1.Want{
		Id:         db.NewID(),
		Query:      strings.TrimSpace(req.GetQuery()),
		Kind:       req.GetKind(),
		SourceId:   req.GetSourceId(),
		GroupMatch: req.GetGroupMatch(),
		FormatId:   req.GetFormatId(),
		AutoPull:   req.GetAutoPull() || req.GetSlotId() != "",
		SlotId:     req.GetSlotId(),
		RuntimeId:  req.GetRuntimeId(),
		Params:     req.GetParams(),
		ProfileId:  profileID,
		CreatedAt:  timestamppb.Now(),
	}
	m.saveWant(w, v1.EventAction_EVENT_ACTION_CREATED)
	// The first look runs behind the reply so a slow source does not hold the caller
	if _, err := m.Check(ctx, w.GetId(), false); err != nil {
		m.Log.Warn("want check failed to start", "id", w.GetId(), "err", err)
	}
	return w, nil
}

// Names the watches and wants pick selects, clearing each through fix when asked
func (m *Manager) referrers(pickWatch func(*v1.Watch) bool, fixWatch func(*v1.Watch), pickWant func(*v1.Want) bool, fixWant func(*v1.Want), clear bool) []string {
	var out []string
	for _, w := range m.List() {
		if !pickWatch(w) {
			continue
		}
		out = append(out, "watch "+w.GetRepo())
		if clear {
			fixWatch(w)
		}
	}
	for _, w := range m.ListWants() {
		if !pickWant(w) {
			continue
		}
		out = append(out, "want "+w.GetQuery())
		if clear {
			fixWant(w)
		}
	}
	return out
}

// Names the watches and wants whose swaps start from a profile, dropping the reference when clear is set
func (m *Manager) ProfileReferrers(refers func(ref, runtimeID string) bool, clear bool) []string {
	return m.referrers(
		func(w *v1.Watch) bool { return refers(w.GetProfileId(), w.GetRuntimeId()) },
		func(w *v1.Watch) { w.ProfileId = ""; m.save(w, v1.EventAction_EVENT_ACTION_UPDATED) },
		func(w *v1.Want) bool { return refers(w.GetProfileId(), w.GetRuntimeId()) },
		func(w *v1.Want) { w.ProfileId = ""; m.saveWant(w, v1.EventAction_EVENT_ACTION_UPDATED) },
		clear)
}

// Names the watches and wants that swap into a slot, dropping the swap when clear is set
func (m *Manager) SlotReferrers(slotID string, clear bool) []string {
	return m.referrers(
		func(w *v1.Watch) bool { return w.GetSlotId() == slotID },
		func(w *v1.Watch) { w.SlotId = ""; m.save(w, v1.EventAction_EVENT_ACTION_UPDATED) },
		func(w *v1.Want) bool { return w.GetSlotId() == slotID },
		func(w *v1.Want) { w.SlotId = ""; m.saveWant(w, v1.EventAction_EVENT_ACTION_UPDATED) },
		clear)
}

// Names the watches of a source and the wants narrowed to it, removing the watches and widening the wants when clear is set
func (m *Manager) SourceReferrers(sourceID string, clear bool) []string {
	return m.referrers(
		func(w *v1.Watch) bool { return w.GetSourceId() == sourceID },
		func(w *v1.Watch) { m.Remove(context.Background(), w.GetId()) },
		func(w *v1.Want) bool { return w.GetSourceId() == sourceID },
		func(w *v1.Want) { w.SourceId = ""; m.saveWant(w, v1.EventAction_EVENT_ACTION_UPDATED) },
		clear)
}

// Removes a want and its findings
func (m *Manager) RemoveWant(ctx context.Context, id string) (*v1.Want, error) {
	w, err := m.findWant(id)
	if err != nil {
		return nil, err
	}
	findings, _ := m.DB.ListFindings(ctx, "", w.GetId(), false)
	if _, err := m.DB.DeleteWant(ctx, w.GetId()); err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.wants, w.GetId())
	m.mu.Unlock()
	m.dropFindings(findings)
	m.Events.Publish(v1.EventKind_EVENT_KIND_WANT, v1.EventAction_EVENT_ACTION_DELETED, w.GetId(), w)
	return w, nil
}

// Searches for a want and takes the first repository with a matching group
func (m *Manager) checkWant(ctx context.Context, log logf, w *v1.Want) error {
	resp, err := m.Inspector.Sources.Search(ctx, &v1.SearchRequest{Query: w.GetQuery(), Kind: w.GetKind(), SourceId: w.GetSourceId(), Limit: wantHits})
	w.CheckedAt = timestamppb.Now()
	if err != nil {
		w.Error = err.Error()
		m.saveWant(w, v1.EventAction_EVENT_ACTION_UPDATED)
		return err
	}
	var re *regexp.Regexp
	if w.GetGroupMatch() != "" {
		re, _ = regexp.Compile(w.GetGroupMatch())
	}
	for _, warn := range resp.GetWarnings() {
		log("%s: %s", w.GetQuery(), warn)
	}
	for _, hit := range resp.GetHits() {
		if ctx.Err() != nil {
			break
		}
		_, model, err := m.Inspector.ResolveFresh(ctx, hit.GetSourceId(), hit.GetRepo(), "")
		if err != nil {
			log("%s: %s did not resolve: %v", w.GetQuery(), hit.GetRepo(), err)
			continue
		}
		for _, g := range m.Inspector.Classifier.Groups(model) {
			if w.GetFormatId() != "" && g.FormatID != w.GetFormatId() || re != nil && !re.MatchString(g.Name) {
				continue
			}
			f := &v1.Finding{Id: db.NewID(), WantId: w.GetId(), SourceId: hit.GetSourceId(), Kind: v1.FindingKind_FINDING_KIND_WANTED_FOUND, Repo: hit.GetRepo(), Commit: model.GetCommit(), Group: g.Name, Detail: fmt.Sprintf("%s has %s %s, wanted as %q", hit.GetSourceId(), hit.GetRepo(), g.Name, w.GetQuery()), FoundAt: timestamppb.Now()}
			w.FoundSourceId, w.FoundRepo, w.FoundGroup, w.Satisfied, w.Error = hit.GetSourceId(), hit.GetRepo(), g.Name, true, ""
			m.saveFinding(ctx, f, v1.EventAction_EVENT_ACTION_CREATED)
			log("%s", f.GetDetail())
			m.saveWant(w, v1.EventAction_EVENT_ACTION_UPDATED)
			if w.GetAutoPull() {
				if task := m.pull(ctx, log, f, hit.GetSourceId(), hit.GetRepo(), "", g.Name); task != "" {
					w.TaskId = task
					m.saveWant(w, v1.EventAction_EVENT_ACTION_UPDATED)
					run := &v1.RunRequest{SourceId: hit.GetSourceId(), Repo: hit.GetRepo(), Group: g.Name, RuntimeId: w.GetRuntimeId(), Params: w.GetParams(), SlotId: w.GetSlotId(), ProfileId: w.GetProfileId()}
					if swap := m.swap(ctx, log, w.GetSlotId(), run, f); swap != "" {
						w.SwapTaskId = swap
						m.saveWant(w, v1.EventAction_EVENT_ACTION_UPDATED)
					}
				}
			}
			m.prune(ctx)
			return nil
		}
	}
	w.Error = ""
	m.saveWant(w, v1.EventAction_EVENT_ACTION_UPDATED)
	log("%s: nothing matching yet across %d hits", w.GetQuery(), len(resp.GetHits()))
	return nil
}

// Pulls one group for a finding and waits, returning the task id on success
func (m *Manager) pull(ctx context.Context, log logf, f *v1.Finding, sourceID, repo, revision, group string) string {
	task, err := m.Puller.Pull(ctx, &v1.PullRequest{SourceId: sourceID, Repo: repo, Revision: revision, Group: group})
	if err != nil {
		log("pull %s %s failed to start: %v", repo, group, err)
		return ""
	}
	f.TaskId = task.GetId()
	m.saveFinding(ctx, f, v1.EventAction_EVENT_ACTION_UPDATED)
	log("pulling %s %s as task %s", repo, group, task.GetId())
	if err := m.Tasks.WaitOK(ctx, task.GetId()); err != nil {
		log("pull %s %s did not succeed: %v", repo, group, err)
		return ""
	}
	return task.GetId()
}

// Swaps a slot onto a run and waits, recording the swap on the findings that asked for it, nothing without a slot
func (m *Manager) swap(ctx context.Context, log logf, slotID string, run *v1.RunRequest, findings ...*v1.Finding) string {
	if slotID == "" {
		return ""
	}
	_, _, task, err := m.Slots.Swap(ctx, &v1.SwapRequest{SlotId: slotID, Run: run})
	if err != nil {
		log("swap into slot %s failed: %v", slotID, err)
		return ""
	}
	log("swapping slot %s to %s %s as task %s", slotID, run.GetRepo(), run.GetGroup(), task.GetId())
	for _, f := range findings {
		f.SwapTaskId = task.GetId()
		m.saveFinding(ctx, f, v1.EventAction_EVENT_ACTION_UPDATED)
	}
	if err := m.Tasks.WaitOK(ctx, task.GetId()); err != nil {
		log("swap did not succeed: %v", err)
	}
	return task.GetId()
}

// Drops old acknowledged findings and tells the stream which
func (m *Manager) prune(ctx context.Context) {
	gone, err := m.DB.PruneFindings(ctx, findingsMax)
	if err != nil {
		m.Log.Warn("finding prune failed", "err", err)
	}
	m.dropFindings(gone)
}

// Adds a watch and records its baseline with one check
func (m *Manager) Add(ctx context.Context, req *v1.AddWatchRequest) (*v1.Watch, error) {
	if strings.TrimSpace(req.GetRepo()) == "" {
		return nil, fmt.Errorf("%w: repo required", ErrWatch)
	}
	src, err := m.Inspector.Sources.Get(req.GetSourceId())
	if err != nil {
		return nil, err
	}
	for _, w := range m.List() {
		if w.GetSourceId() == src.Spec().GetId() && w.GetRepo() == req.GetRepo() && w.GetRevision() == req.GetRevision() {
			return nil, fmt.Errorf("%w: %s is already watched as %s", ErrWatch, req.GetRepo(), w.GetId())
		}
	}
	profileID, err := m.target(req.GetGroupMatch(), req.GetSlotId(), req.GetRuntimeId(), req.GetProfileId())
	if err != nil {
		return nil, err
	}
	// No id until the baseline check passes, so a failed add leaves nothing behind
	w := &v1.Watch{
		SourceId:   src.Spec().GetId(),
		Repo:       req.GetRepo(),
		Revision:   req.GetRevision(),
		GroupMatch: req.GetGroupMatch(),
		AutoPull:   req.GetAutoPull(),
		SlotId:     req.GetSlotId(),
		RuntimeId:  req.GetRuntimeId(),
		Params:     req.GetParams(),
		ProfileId:  profileID,
		CreatedAt:  timestamppb.Now(),
	}
	if err := m.check(ctx, nil, w); err != nil {
		return nil, err
	}
	w.Id = db.NewID()
	m.save(w, v1.EventAction_EVENT_ACTION_CREATED)
	return w, nil
}

// Removes a watch and its findings
func (m *Manager) Remove(ctx context.Context, id string) (*v1.Watch, error) {
	w, err := m.find(id)
	if err != nil {
		return nil, err
	}
	findings, _ := m.Findings(ctx, w.GetId(), "", false)
	if _, err := m.DB.DeleteWatch(ctx, w.GetId()); err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.watches, w.GetId())
	m.mu.Unlock()
	m.dropFindings(findings)
	m.Events.Publish(v1.EventKind_EVENT_KIND_WATCH, v1.EventAction_EVENT_ACTION_DELETED, w.GetId(), w)
	return w, nil
}

// Starts a task checking one watch or want by id, or every watch and open want
// when empty, a satisfied want only when rearmed, which forgets what it found
func (m *Manager) Check(ctx context.Context, id string, rearm bool) (*v1.Task, error) {
	var watches []*v1.Watch
	var wants []*v1.Want
	title := "check " + id
	switch {
	case id == "":
		watches, wants = m.List(), m.openWants()
		title = fmt.Sprintf("check %d watches and %d wants", len(watches), len(wants))
	default:
		if w, err := m.find(id); err == nil {
			watches, title = []*v1.Watch{w}, "check "+w.GetRepo()
		} else if wt, err := m.findWant(id); err == nil {
			if wt.GetSatisfied() && !rearm {
				return nil, fmt.Errorf("%w: %q is satisfied by %s %s, rearm it to look again", ErrWatch, wt.GetQuery(), wt.GetFoundRepo(), wt.GetFoundGroup())
			}
			if wt.GetSatisfied() {
				wt.Satisfied, wt.FoundSourceId, wt.FoundRepo, wt.FoundGroup, wt.TaskId, wt.Error = false, "", "", "", "", ""
				m.saveWant(wt, v1.EventAction_EVENT_ACTION_UPDATED)
			}
			wants, title = []*v1.Want{wt}, "look for "+wt.GetQuery()
		} else {
			return nil, fmt.Errorf("%w %q", ErrUnknownWatch, id)
		}
	}
	m.mu.Lock()
	if m.checking && id == "" {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: a check is already running", ErrWatch)
	}
	if id != "" && m.busy[id] {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %s is being checked right now", ErrWatch, title)
	}
	// One check owns each id it touches, so a scheduled check never doubles a check by hand
	if m.busy == nil {
		m.busy = map[string]bool{}
	}
	watches = slices.DeleteFunc(watches, func(w *v1.Watch) bool { return m.busy[w.GetId()] })
	wants = slices.DeleteFunc(wants, func(w *v1.Want) bool { return m.busy[w.GetId()] })
	held := make([]string, 0, len(watches)+len(wants))
	for _, w := range watches {
		held = append(held, w.GetId())
	}
	for _, w := range wants {
		held = append(held, w.GetId())
	}
	for _, h := range held {
		m.busy[h] = true
	}
	m.checking = id == ""
	m.mu.Unlock()
	total := uint64(len(watches) + len(wants))
	return m.Tasks.Start(kindCheck, title, map[string]string{"watch": id}, func(ctx context.Context, h *tasks.Handle) error {
		defer func() {
			m.mu.Lock()
			for _, h := range held {
				delete(m.busy, h)
			}
			if id == "" {
				m.checking = false
			}
			m.mu.Unlock()
		}()
		var failed []string
		done := uint64(0)
		for _, w := range watches {
			h.Progress(done, total, "checking "+w.GetRepo())
			if err := m.check(ctx, h.Logf, w); err != nil {
				failed = append(failed, w.GetRepo()+": "+err.Error())
			}
			done++
		}
		for _, w := range wants {
			h.Progress(done, total, "looking for "+w.GetQuery())
			if err := m.checkWant(ctx, h.Logf, w); err != nil {
				failed = append(failed, w.GetQuery()+": "+err.Error())
			}
			done++
		}
		h.Progress(total, total, "done")
		if len(failed) > 0 {
			return errors.New(strings.Join(failed, "; "))
		}
		return nil
	}), nil
}

// Resolves a watch, records changes, and runs automatic actions
func (m *Manager) check(ctx context.Context, log logf, w *v1.Watch) error {
	if log == nil {
		log = func(string, ...any) {}
	}
	_, model, err := m.Inspector.ResolveFresh(ctx, w.GetSourceId(), w.GetRepo(), w.GetRevision())
	if err != nil {
		w.Error = err.Error()
		w.CheckedAt = timestamppb.Now()
		if w.GetId() != "" {
			m.save(w, v1.EventAction_EVENT_ACTION_UPDATED)
		}
		return err
	}
	var groups []string
	for _, g := range m.Inspector.Classifier.Groups(model) {
		groups = append(groups, g.Name)
	}
	sort.Strings(groups)
	baseline := w.GetLastCommit() == "" && len(w.GetKnownGroups()) == 0
	var findings []*v1.Finding
	newFinding := func(kind v1.FindingKind, group, detail string) {
		findings = append(findings, &v1.Finding{Id: db.NewID(), WatchId: w.GetId(), SourceId: w.GetSourceId(), Kind: kind, Repo: w.GetRepo(), Commit: model.GetCommit(), Group: group, Detail: detail, FoundAt: timestamppb.Now()})
	}
	if !baseline {
		if model.GetCommit() != "" && model.GetCommit() != w.GetLastCommit() {
			newFinding(v1.FindingKind_FINDING_KIND_NEW_REVISION, "", fmt.Sprintf("commit %s replaced %s", short(model.GetCommit()), short(w.GetLastCommit())))
		}
		for _, g := range groups {
			if !slices.Contains(w.GetKnownGroups(), g) {
				newFinding(v1.FindingKind_FINDING_KIND_NEW_GROUP, g, "new weight group "+g)
			}
		}
		for _, g := range w.GetKnownGroups() {
			if !slices.Contains(groups, g) {
				newFinding(v1.FindingKind_FINDING_KIND_REMOVED_GROUP, g, "weight group "+g+" is gone")
			}
		}
	}
	w.LastCommit, w.KnownGroups, w.CheckedAt, w.Error = model.GetCommit(), groups, timestamppb.Now(), ""
	if w.GetId() != "" {
		m.save(w, v1.EventAction_EVENT_ACTION_UPDATED)
	}
	for _, f := range findings {
		m.saveFinding(ctx, f, v1.EventAction_EVENT_ACTION_CREATED)
		log("%s: %s", w.GetRepo(), f.GetDetail())
	}
	if baseline {
		log("%s: baseline commit %s with %d groups", w.GetRepo(), short(model.GetCommit()), len(groups))
	} else if len(findings) == 0 {
		log("%s: unchanged at %s", w.GetRepo(), short(model.GetCommit()))
	}
	if w.GetAutoPull() && len(findings) > 0 {
		m.act(ctx, log, w, findings)
	}
	m.prune(ctx)
	return nil
}

// Pulls groups the findings touch, then swaps the slot
func (m *Manager) act(ctx context.Context, log logf, w *v1.Watch, findings []*v1.Finding) {
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
	var asked []*v1.Finding
	for _, g := range targets {
		if m.pull(ctx, log, wanted[g], w.GetSourceId(), w.GetRepo(), w.GetRevision(), g) != "" {
			pulled = append(pulled, g)
			asked = append(asked, wanted[g])
		}
	}
	if len(pulled) == 0 {
		return
	}
	group := pulled[0]
	if slices.Contains(pulled, served) {
		group = served
	}
	m.swap(ctx, log, w.GetSlotId(), &v1.RunRequest{SourceId: w.GetSourceId(), Repo: w.GetRepo(), Group: group, RuntimeId: w.GetRuntimeId(), Params: w.GetParams(), SlotId: w.GetSlotId(), ProfileId: w.GetProfileId()}, asked...)
}

// Lists findings newest first, for one watch by id or repo, or one want by id, when named
func (m *Manager) Findings(ctx context.Context, watchID, wantID string, unackedOnly bool) ([]*v1.Finding, error) {
	if watchID != "" {
		if w, err := m.find(watchID); err == nil {
			watchID = w.GetId()
		} else if wt, werr := m.findWant(watchID); werr == nil {
			watchID, wantID = "", wt.GetId()
		} else {
			return nil, err
		}
	}
	if wantID != "" {
		if _, err := m.findWant(wantID); err != nil {
			return nil, err
		}
	}
	return m.DB.ListFindings(ctx, watchID, wantID, unackedOnly)
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
	m.Events.Publish(v1.EventKind_EVENT_KIND_FINDING, v1.EventAction_EVENT_ACTION_UPDATED, f.GetId(), f)
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
