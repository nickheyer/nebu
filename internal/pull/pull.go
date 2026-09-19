// Package pull moves weight groups from sources into the store.
package pull

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/transfer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	kindPull   = "pull"
	kindVerify = "verify"
)

// Resolves and downloads groups into the store.
type Puller struct {
	Inspector *inspect.Inspector
	Store     *store.Store
	Fetcher   *transfer.Fetcher
	Tasks     *tasks.Manager
	Events    *events.Bus
	// Models protected from eviction.
	Keep func(*v1.StoredModel) bool
	// Bytes the store's filesystem keeps free after a pull
	MinFree uint64
}

// One group to download and store.
type landing struct {
	src   sources.Source
	model *v1.Model
	g     *formats.Group
	// Selected files. Nil selects the whole group.
	files []*v1.Artifact
	// Descriptor from headers, or nil if unreadable.
	descriptor *v1.Descriptor
	// Companion slot, or empty for the model itself.
	slot string
}

func (l *landing) artifacts() []*v1.Artifact {
	if l.files != nil {
		return l.files
	}
	return inspect.GroupArtifacts(l.g)
}

// Returns total bytes and bytes missing from the store.
func (p *Puller) sizes(landings []*landing) (total, need uint64) {
	for _, l := range landings {
		for _, a := range l.artifacts() {
			total += a.GetSizeBytes()
			if a.GetSha256() == "" || !p.Store.HasBlob(store.Digest(a.GetSha256())) {
				need += a.GetSizeBytes()
			}
		}
	}
	return total, need
}

// Checks disk space, including space recoverable through eviction.
func (p *Puller) room(landings []*landing) error {
	_, need := p.sizes(landings)
	st, err := host.Stat(p.Store.Root())
	if err != nil {
		return nil
	}
	free := st.GetFreeBytes() + p.Store.Evictable(need, p.Keep)
	if need+p.MinFree <= free {
		return nil
	}
	return fmt.Errorf("%w: needs %s, %s free on %s", store.ErrNoRoom, estimate.Human(need), estimate.Human(st.GetFreeBytes()), st.GetPath())
}

// Pull downloads a group and its missing blueprint parts. Unreadable headers or
// unresolved required parts fail before the task starts. Alone skips those checks
// and downloads only the requested group.
func (p *Puller) Pull(ctx context.Context, req *v1.PullRequest) (*v1.Task, error) {
	src, model, err := p.Inspector.Resolve(ctx, req.GetSourceId(), req.GetRepo(), req.GetRevision())
	if err != nil {
		return nil, err
	}
	g, err := formats.FindGroup(p.Inspector.Formats.Groups(model), req.GetGroup())
	if err != nil {
		return nil, err
	}
	own := &landing{src: src, model: model, g: g}
	landings := []*landing{own}
	var unread error
	var plan *inspect.Plan
	if own.descriptor, err = p.Inspector.Describe(ctx, src, model, g); err != nil {
		if !req.GetAlone() {
			return nil, fmt.Errorf("%s %s: cannot plan parts without readable headers. Use --alone to download only this group: %w", model.GetRepo(), g.Name, err)
		}
		unread = err
	} else if !req.GetAlone() {
		if plan, err = p.Inspector.Parts(ctx, src, model, g, own.descriptor); err != nil {
			return nil, err
		}
		if err := plan.Unfilled(); err != nil {
			return nil, err
		}
		for _, pick := range plan.Pulls() {
			landings = append(landings, &landing{src: pick.Source, model: pick.Model, g: pick.Group, files: pick.Files, descriptor: pick.Descriptor, slot: pick.Fill.Slot})
		}
	}
	if err := p.room(landings); err != nil {
		return nil, err
	}
	labels := map[string]string{"source": model.GetSourceId(), "repo": model.GetRepo(), "group": g.Name, "parts": strconv.Itoa(len(landings) - 1)}
	title := fmt.Sprintf("pull %s %s", model.GetRepo(), g.Name)
	if n := len(landings) - 1; n == 1 {
		title += " and 1 part"
	} else if n > 1 {
		title += fmt.Sprintf(" and %d parts", n)
	}
	return p.Tasks.Start(kindPull, title, labels, func(ctx context.Context, h *tasks.Handle) error {
		if unread != nil {
			h.Logf("%s: unreadable header, downloading with --alone: %v", g.Name, unread)
		}
		if plan != nil {
			for _, pick := range plan.Picks {
				switch {
				case pick.Stored != nil:
					h.Logf("%s (%s): %s %s already stored", pick.Fill.Slot, pick.Fill.Name, pick.Stored.GetRepo(), pick.Stored.GetGroup())
				case pick.Bundled:
					h.Logf("%s (%s): bundled", pick.Fill.Slot, pick.Fill.Name)
				case pick.Err != nil:
					h.Logf("%s (%s, not required): %v", pick.Fill.Slot, pick.Fill.Name, pick.Err)
				}
			}
		}
		return p.run(ctx, h, landings)
	}), nil
}

// Downloads the model and its parts with shared progress.
func (p *Puller) run(ctx context.Context, h *tasks.Handle, landings []*landing) error {
	total, need := p.sizes(landings)
	h.Progress(0, 0, "resolving")
	// Evict least recently used models, protecting groups in this pull.
	keys := map[string]bool{}
	for _, l := range landings {
		keys[store.Key(l.model.GetSourceId(), l.model.GetRepo(), l.g.Name)] = true
	}
	evicted, release, err := p.Store.Evict(need, func(m *v1.StoredModel) bool {
		return keys[store.Key(m.GetSourceId(), m.GetRepo(), m.GetGroup())] || (p.Keep != nil && p.Keep(m))
	})
	defer release()
	for _, m := range evicted {
		h.Logf("evicted %s %s, unused since %s", m.GetRepo(), m.GetGroup(), store.LastUse(m).Format(time.RFC3339))
		p.Events.Publish(v1.EventKind_EVENT_KIND_MODEL, v1.EventAction_EVENT_ACTION_DELETED, store.Key(m.GetSourceId(), m.GetRepo(), m.GetGroup()), m)
	}
	if len(evicted) > 0 {
		p.AnnounceStore()
	}
	if err != nil {
		return fmt.Errorf("evict: %w", err)
	}
	// Block garbage collection until all manifests are written.
	defer p.Store.Hold()()
	h.Progress(0, total, "fetching")
	for _, l := range landings {
		if l.slot != "" {
			h.Logf("%s: pulling %s %s", l.slot, l.model.GetRepo(), l.g.Name)
		}
		if err := p.land(ctx, h, l); err != nil {
			if l.slot != "" {
				return fmt.Errorf("%s from %s %s: %w", l.slot, l.model.GetRepo(), l.g.Name, err)
			}
			return err
		}
	}
	h.Progress(total, total, "done")
	return nil
}

// AnnounceStore publishes store counters.
func (p *Puller) AnnounceStore() {
	st, err := p.Store.Status()
	if err != nil {
		return
	}
	p.Events.Publish(v1.EventKind_EVENT_KIND_STORE, v1.EventAction_EVENT_ACTION_UPDATED, st.GetPath(), st)
}

// Downloads a group, links its files, and writes its manifest.
func (p *Puller) land(ctx context.Context, h *tasks.Handle, l *landing) error {
	src, model, g := l.src, l.model, l.g
	key := store.Key(model.GetSourceId(), model.GetRepo(), g.Name)
	unlock := p.Store.Lock(key)
	defer unlock()
	stored := &v1.StoredModel{
		SourceId: model.GetSourceId(),
		Repo:     model.GetRepo(),
		Revision: model.GetRevision(),
		Commit:   model.GetCommit(),
		Group:    g.Name,
		FormatId: g.FormatID,
	}
	if l.descriptor != nil {
		stored.Descriptor_ = l.descriptor
		stored.Runtimes = p.Inspector.Runtimes.Mask(g.FormatID, l.descriptor.GetKind())
	} else if l.files == nil {
		h.Logf("%s %s: descriptor unavailable", model.GetRepo(), g.Name)
	}
	// Store partial groups as components, such as a tokenizer.
	if l.files != nil && l.slot != "" {
		stored.Descriptor_ = &v1.Descriptor{FormatId: g.FormatID, Group: g.Name, Architecture: strings.TrimPrefix(l.slot, "text_encoder."), Kind: v1.ModelKind_MODEL_KIND_COMPONENT}
		stored.Runtimes = 0
	}
	for _, a := range l.artifacts() {
		if err := ctx.Err(); err != nil {
			return err
		}
		digest, err := p.fetch(ctx, h, src, model, a)
		if err != nil {
			return fmt.Errorf("%s: %w", a.GetPath(), err)
		}
		// Preserve paths relative to the group's root.
		rel := a.GetPath()
		if g.Root != "" {
			rel = strings.TrimPrefix(rel, g.Root+"/")
		}
		linked, err := p.Store.Link(model.GetSourceId(), model.GetRepo(), g.Name, rel, digest)
		if err != nil {
			return err
		}
		stored.Artifacts = append(stored.Artifacts, &v1.StoredArtifact{Artifact: a, Digest: digest, Path: linked})
		stored.Bytes += a.GetSizeBytes()
	}
	dir, err := p.Store.GroupDir(model.GetSourceId(), model.GetRepo(), g.Name)
	if err != nil {
		return err
	}
	stored.Path = dir
	stored.PulledAt = timestamppb.Now()
	if err := p.Store.WriteManifest(stored); err != nil {
		return err
	}
	if err := p.Store.PruneLinks(stored); err != nil {
		return err
	}
	p.Events.Publish(v1.EventKind_EVENT_KIND_MODEL, v1.EventAction_EVENT_ACTION_CREATED, key, stored)
	p.AnnounceStore()
	return nil
}

// Ensures one artifact's blob exists and returns its digest
func (p *Puller) fetch(ctx context.Context, h *tasks.Handle, src sources.Source, model *v1.Model, a *v1.Artifact) (string, error) {
	size := int64(a.GetSizeBytes())
	blob, err := src.Open(ctx, model, a)
	if err != nil {
		return "", err
	}
	defer blob.Close()
	// Use ranged downloads when rate limits or pauses apply.
	if r, ok := blob.(sources.Ranged); ok && p.Fetcher.Schedule.Limits() {
		blob = r.Ranged()
	}
	if whole, ok := blob.(sources.Materializer); ok {
		// Reuse local blobs. Apply pause windows to downloads.
		if _, onDisk := blob.(interface{ Name() string }); !onDisk {
			if err := p.Fetcher.Hold(ctx); err != nil {
				return "", err
			}
		}
		h.Message("fetching " + a.GetPath())
		var moved int64
		path, err := whole.Materialize(ctx, func(d int64) {
			moved += d
			h.Add(d)
		})
		if err != nil {
			return "", err
		}
		h.Message("hashing " + a.GetPath())
		hexDigest, err := transfer.HashFile(path, func(d int64) {
			if moved == 0 {
				h.Add(d)
			}
		})
		if err != nil {
			return "", err
		}
		if a.GetSha256() != "" && hexDigest != a.GetSha256() {
			return "", fmt.Errorf("%w: got %s want %s", transfer.ErrDigestMismatch, hexDigest, a.GetSha256())
		}
		digest := store.Digest(hexDigest)
		unlock := p.Store.Lock("blob:" + digest)
		defer unlock()
		if p.Store.HasBlob(digest) {
			h.Logf("%s already stored", a.GetPath())
			return digest, nil
		}
		if err := p.Store.Adopt(path, digest); err != nil {
			return "", err
		}
		h.Logf("%s adopted as %s", a.GetPath(), digest)
		return digest, nil
	}
	key := partialKey(model, a)
	unlock := p.Store.Lock("blob:" + key)
	defer unlock()
	if a.GetSha256() != "" && p.Store.HasBlob(store.Digest(a.GetSha256())) {
		h.Add(size)
		h.Logf("%s already stored", a.GetPath())
		return store.Digest(a.GetSha256()), nil
	}
	h.Message("downloading " + a.GetPath())
	hexDigest, err := p.Fetcher.Fetch(ctx, blob, p.Store.PartialPath(key), a.GetSha256(), func(d int64) { h.Add(d) })
	if err != nil {
		return "", err
	}
	digest := store.Digest(hexDigest)
	if err := p.Store.Commit(p.Store.PartialPath(key), digest); err != nil {
		return "", err
	}
	h.Logf("%s verified %s", a.GetPath(), digest)
	return digest, nil
}

func partialKey(model *v1.Model, a *v1.Artifact) string {
	if a.GetSha256() != "" {
		return "sha256-" + strings.ToLower(a.GetSha256())
	}
	sum := sha256.Sum256([]byte(model.GetSourceId() + "\x00" + model.GetRepo() + "\x00" + a.GetPath()))
	return "pending-" + hex.EncodeToString(sum[:16])
}

// Returns requested stored models, or all models for an empty request.
func (p *Puller) selectManifests(sourceID, repo, group string) ([]*v1.StoredModel, error) {
	manifests, err := p.Store.ListManifests()
	if err != nil {
		return nil, err
	}
	var selected []*v1.StoredModel
	for _, m := range manifests {
		if sourceID != "" && m.GetSourceId() != sourceID || repo != "" && m.GetRepo() != repo || group != "" && m.GetGroup() != group {
			continue
		}
		selected = append(selected, m)
	}
	if len(selected) == 0 && (sourceID != "" || repo != "" || group != "") {
		return nil, fmt.Errorf("%w: nothing matches %s %s %s", store.ErrNotStored, sourceID, repo, group)
	}
	return selected, nil
}

// Total bytes for task progress.
func sizeOf(models []*v1.StoredModel) uint64 {
	var total uint64
	for _, m := range models {
		total += m.GetBytes()
	}
	return total
}

// Starts a task that rehashes blobs and drops corrupt ones
func (p *Puller) Verify(ctx context.Context, req *v1.VerifyRequest) (*v1.Task, error) {
	selected, err := p.selectManifests(req.GetSourceId(), req.GetRepo(), req.GetGroup())
	if err != nil {
		return nil, err
	}
	title := fmt.Sprintf("verify %d models", len(selected))
	return p.Tasks.Start(kindVerify, title, nil, func(ctx context.Context, h *tasks.Handle) error {
		return p.verify(ctx, h, selected)
	}), nil
}

func (p *Puller) verify(ctx context.Context, h *tasks.Handle, models []*v1.StoredModel) error {
	total := sizeOf(models)
	h.Progress(0, total, "hashing")
	bad := 0
	for _, m := range models {
		for _, sa := range m.GetArtifacts() {
			if err := ctx.Err(); err != nil {
				return err
			}
			h.Message("hashing " + sa.GetArtifact().GetPath())
			if !p.Store.HasBlob(sa.GetDigest()) {
				bad++
				h.Logf("%s %s missing blob %s", m.GetRepo(), sa.GetArtifact().GetPath(), sa.GetDigest())
				h.Add(int64(sa.GetArtifact().GetSizeBytes()))
				continue
			}
			// Block concurrent downloads while verifying this blob.
			unlock := p.Store.Lock("blob:" + sa.GetDigest())
			got, err := transfer.HashFile(p.Store.BlobPath(sa.GetDigest()), func(d int64) { h.Add(d) })
			if err != nil {
				unlock()
				return err
			}
			if store.Digest(got) != sa.GetDigest() {
				bad++
				h.Logf("%s %s corrupt, removing blob %s", m.GetRepo(), sa.GetArtifact().GetPath(), sa.GetDigest())
				if err := p.Store.RemoveBlob(sa.GetDigest()); err != nil {
					unlock()
					return err
				}
				p.Events.Publish(v1.EventKind_EVENT_KIND_MODEL, v1.EventAction_EVENT_ACTION_UPDATED, store.Key(m.GetSourceId(), m.GetRepo(), m.GetGroup()), m)
			}
			unlock()
		}
	}
	h.Progress(total, total, "done")
	if bad > 0 {
		p.AnnounceStore()
		return fmt.Errorf("%d artifacts failed verification, pull again to repair", bad)
	}
	return nil
}
