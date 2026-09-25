package pull

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/transfer"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A mesh member holding a model, with the connection its blobs are read over
type Holder struct {
	NodeID, Name string
	// The member's API base and a client carrying the session credential
	Base string
	HTTP *http.Client
	// Every model the member holds, so a pipeline's parts are found beside it
	Stored []*v1.StoredSummary
}

// Where pulls look before the internet: members holding the model, best link first
type Mesh interface {
	// Members holding a model. An empty group takes any group of the repo, an empty source any source.
	Holders(ctx context.Context, sourceID, repo, group string) []Holder
	// The manifest a member holds for a model
	Manifest(ctx context.Context, h Holder, sourceID, repo, group string) (*v1.StoredModel, error)
}

// A group landing from members' stores
type meshLanding struct {
	// Members holding the group, best link first: each blob comes from the first that serves it
	holders []Holder
	// The manifest, from the first member that handed over one matching its record
	stored *v1.StoredModel
	// Selected files, nil for the whole group
	files []*v1.Artifact
	// Companion slot, or empty for the model itself
	slot string
	// The descriptor read from the source when the member's manifest carries none
	descriptor *v1.Descriptor
	// The same group at its source, for blobs no member serves. Nil when the source did not answer.
	net *landing
}

// The member's artifacts this landing takes: the selected files, or every one
func (l *meshLanding) artifacts() []*v1.StoredArtifact {
	if l.files == nil {
		return l.stored.GetArtifacts()
	}
	var out []*v1.StoredArtifact
	for _, want := range l.files {
		for _, sa := range l.stored.GetArtifacts() {
			if sa.GetArtifact().GetPath() == want.GetPath() {
				out = append(out, sa)
				break
			}
		}
	}
	return out
}

// Whether a member's manifest holds every selected file, by path and by digest when the
// selection names one
func holdsFiles(m *v1.StoredModel, files []*v1.Artifact) bool {
	for _, want := range files {
		found := false
		for _, sa := range m.GetArtifacts() {
			if sa.GetArtifact().GetPath() != want.GetPath() {
				continue
			}
			if want.GetSha256() != "" && sa.GetDigest() != store.Digest(want.GetSha256()) {
				continue
			}
			found = true
			break
		}
		if !found {
			return false
		}
	}
	return true
}

// Whether a member's summary is the model a request names: the repo, the source and group when
// named, and the revision when named, by name or by commit
func summaryMatches(s *v1.StoredSummary, req *v1.PullRequest) bool {
	if s.GetRepo() != req.GetRepo() {
		return false
	}
	if req.GetSourceId() != "" && s.GetSourceId() != req.GetSourceId() {
		return false
	}
	if req.GetGroup() != "" && s.GetGroup() != req.GetGroup() {
		return false
	}
	if rev := req.GetRevision(); rev != "" && s.GetRevision() != rev && s.GetCommit() != rev {
		return false
	}
	return true
}

// Members holding the requested model, best link first, and the request completed from what they
// hold: a request naming no group takes the one group members keep for the repo, and a repo held
// as several groups or from several sources must be named
func (p *Puller) meshHolders(ctx context.Context, req *v1.PullRequest) (*v1.PullRequest, []Holder, error) {
	holders := p.Mesh.Holders(ctx, req.GetSourceId(), req.GetRepo(), req.GetGroup())
	type key struct{ source, group string }
	var keys []key
	seen := map[key]bool{}
	for _, h := range holders {
		for _, s := range h.Stored {
			if !summaryMatches(s, req) {
				continue
			}
			k := key{s.GetSourceId(), s.GetGroup()}
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		return req, nil, nil
	}
	if len(keys) > 1 {
		names := make([]string, 0, len(keys))
		for _, k := range keys {
			names = append(names, k.source+" "+k.group)
		}
		sort.Strings(names)
		return nil, nil, fmt.Errorf("%w: members hold %s as %s, name one with --source and --group", formats.ErrUnknownGroup, req.GetRepo(), strings.Join(names, ", "))
	}
	named := proto.Clone(req).(*v1.PullRequest)
	named.SourceId, named.Group = keys[0].source, keys[0].group
	var held []Holder
	for _, h := range holders {
		for _, s := range h.Stored {
			if summaryMatches(s, named) {
				held = append(held, h)
				break
			}
		}
	}
	return named, held, nil
}

// Pulls a group from the members holding it, and its missing parts from members when they hold
// them and from the internet otherwise. Every blob comes from the first member that serves it in
// link order, then from the internet when none does and the source answers.
func (p *Puller) pullFromMesh(ctx context.Context, req *v1.PullRequest, holders []Holder) (*v1.Task, error) {
	var notes []string
	own, err := p.meshLanding(ctx, holders, req.GetSourceId(), req.GetRepo(), req.GetGroup(), nil, "", &notes)
	if err != nil {
		return nil, err
	}
	src, model, g, rerr := p.resolveGroup(ctx, own.stored)
	if rerr == nil {
		own.net = &landing{src: src, model: model, g: g}
	}
	d := own.stored.GetDescriptor_()
	if d == nil && rerr == nil {
		described, err := p.Inspector.Describe(ctx, src, model, g)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s %s: no member's manifest carries a descriptor and %s has no readable header: %v", own.stored.GetRepo(), own.stored.GetGroup(), own.stored.GetSourceId(), err))
		} else {
			d, own.descriptor = described, described
		}
	}
	landings := []*meshLanding{own}
	var fromNet []*landing
	if !req.GetAlone() {
		switch {
		case rerr == nil && d != nil:
			plan, err := p.Inspector.Parts(ctx, src, model, g, d)
			if err != nil {
				return nil, err
			}
			if err := plan.Unfilled(); err != nil {
				return nil, err
			}
			for _, pick := range plan.Pulls() {
				net := &landing{src: pick.Source, model: pick.Model, g: pick.Group, files: pick.Files, descriptor: pick.Descriptor, slot: pick.Fill.Slot}
				hs := p.Mesh.Holders(ctx, pick.Model.GetSourceId(), pick.Model.GetRepo(), pick.Group.Name)
				if len(hs) == 0 {
					fromNet = append(fromNet, net)
					continue
				}
				ml, err := p.meshLanding(ctx, hs, pick.Model.GetSourceId(), pick.Model.GetRepo(), pick.Group.Name, pick.Files, pick.Fill.Slot, &notes)
				if err != nil {
					notes = append(notes, fmt.Sprintf("%s: %v, pulling from %s", pick.Fill.Slot, err, pick.Model.GetSourceId()))
					fromNet = append(fromNet, net)
					continue
				}
				ml.net = net
				landings = append(landings, ml)
			}
		default:
			if rerr != nil {
				notes = append(notes, fmt.Sprintf("%s did not answer, parts come from what members keep beside the model: %v", own.stored.GetSourceId(), rerr))
			} else {
				notes = append(notes, fmt.Sprintf("%s %s has no descriptor to plan parts from, parts come from what members keep beside the model", own.stored.GetRepo(), own.stored.GetGroup()))
			}
			type key struct{ source, group string }
			seen := map[key]bool{}
			for _, h := range holders {
				for _, s := range h.Stored {
					if s.GetRepo() != own.stored.GetRepo() || s.GetGroup() == own.stored.GetGroup() || s.GetKind() != v1.ModelKind_MODEL_KIND_COMPONENT {
						continue
					}
					k := key{s.GetSourceId(), s.GetGroup()}
					if seen[k] {
						continue
					}
					seen[k] = true
					hs := p.Mesh.Holders(ctx, k.source, s.GetRepo(), k.group)
					ml, err := p.meshLanding(ctx, hs, k.source, s.GetRepo(), k.group, nil, "", &notes)
					if err != nil {
						return nil, err
					}
					ml.slot = ml.stored.GetDescriptor_().GetArchitecture()
					landings = append(landings, ml)
				}
			}
		}
	}
	if err := p.roomFor(landings, fromNet); err != nil {
		return nil, err
	}
	parts := len(landings) + len(fromNet) - 1
	labels := map[string]string{"source": own.stored.GetSourceId(), "repo": own.stored.GetRepo(), "group": own.stored.GetGroup(), "parts": strconv.Itoa(parts), "from": own.holders[0].NodeID}
	title := fmt.Sprintf("pull %s %s from %s", own.stored.GetRepo(), own.stored.GetGroup(), own.holders[0].Name)
	if parts == 1 {
		title += " and 1 part"
	} else if parts > 1 {
		title += fmt.Sprintf(" and %d parts", parts)
	}
	return p.Tasks.Start(kindPull, title, labels, func(ctx context.Context, h *tasks.Handle) error {
		for _, n := range notes {
			h.Logf("%s", n)
		}
		return p.runMesh(ctx, h, landings, fromNet)
	}), nil
}

// The source's view of a model members hold: the group as the source lists it now, for its parts
// and for blobs no member serves
func (p *Puller) resolveGroup(ctx context.Context, m *v1.StoredModel) (sources.Source, *v1.Model, *formats.Group, error) {
	src, model, err := p.Inspector.Resolve(ctx, m.GetSourceId(), m.GetRepo(), m.GetRevision())
	if err != nil {
		return nil, nil, nil, err
	}
	g := findGroup(p.Inspector.Formats.Groups(model), m.GetGroup())
	if g == nil {
		return nil, nil, nil, fmt.Errorf("%s has no group %s at %s", model.GetRepo(), m.GetGroup(), model.GetRevision())
	}
	return src, model, g, nil
}

// Plans one group from members: the manifest from the first that hands over one matching its
// record and holding every selected file, the blobs from any of them in link order. Members
// skipped are noted for the task log.
func (p *Puller) meshLanding(ctx context.Context, holders []Holder, sourceID, repo, group string, files []*v1.Artifact, slot string, notes *[]string) (*meshLanding, error) {
	var skipped []string
	for _, h := range holders {
		m, err := p.Mesh.Manifest(ctx, h, sourceID, repo, group)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: manifest of %s %s: %v", h.Name, repo, group, err))
			continue
		}
		if err := digestMatches(h, m); err != nil {
			skipped = append(skipped, err.Error())
			continue
		}
		if files != nil && !holdsFiles(m, files) {
			skipped = append(skipped, fmt.Sprintf("%s holds %s %s without every file wanted", h.Name, repo, group))
			continue
		}
		*notes = append(*notes, skipped...)
		return &meshLanding{holders: holders, stored: m, files: files, slot: slot}, nil
	}
	return nil, fmt.Errorf("no member handed over %s %s: %s", repo, group, strings.Join(skipped, "; "))
}

// Checks the manifest a member handed over against the descriptor digest its record advertised,
// so a record behind its store is caught before its blobs land
func digestMatches(h Holder, m *v1.StoredModel) error {
	var advertised string
	for _, s := range h.Stored {
		if s.GetSourceId() == m.GetSourceId() && s.GetRepo() == m.GetRepo() && s.GetGroup() == m.GetGroup() {
			advertised = s.GetDescriptorDigest()
		}
	}
	got := store.DescriptorDigest(m.GetDescriptor_())
	if advertised == got {
		return nil
	}
	return fmt.Errorf("%s advertises %s %s with descriptor %s but hands over %s, its record is behind its store; sync again", h.Name, m.GetRepo(), m.GetGroup(), short(advertised), short(got))
}

// The first characters of a digest, "none" for an empty one
func short(digest string) string {
	if digest == "" {
		return "none"
	}
	return digest[:min(12, len(digest))]
}

// Finds a group by name
func findGroup(groups []*groupT, name string) *groupT {
	for _, g := range groups {
		if g.Name == name {
			return g
		}
	}
	return nil
}

// Bytes a set of mesh landings still needs
func meshNeed(landings []*meshLanding, hasBlob func(string) bool) (total, need uint64) {
	for _, l := range landings {
		for _, sa := range l.artifacts() {
			total += sa.GetArtifact().GetSizeBytes()
			if !hasBlob(sa.GetDigest()) {
				need += sa.GetArtifact().GetSizeBytes()
			}
		}
	}
	return total, need
}

// Bytes needed for mesh and internet landings together, checked against the store's room
func (p *Puller) roomFor(landings []*meshLanding, fromNet []*landing) error {
	_, need := meshNeed(landings, p.Store.HasBlob)
	_, netNeed := p.sizes(fromNet)
	return p.roomBytes(need + netNeed)
}

// Lands groups from members and the internet with shared progress
func (p *Puller) runMesh(ctx context.Context, h *tasks.Handle, landings []*meshLanding, fromNet []*landing) error {
	total, need := meshNeed(landings, p.Store.HasBlob)
	netTotal, netNeed := p.sizes(fromNet)
	total, need = total+netTotal, need+netNeed
	h.Progress(0, 0, "resolving")
	keys := map[string]bool{}
	for _, l := range landings {
		keys[store.Key(l.stored.GetSourceId(), l.stored.GetRepo(), l.stored.GetGroup())] = true
	}
	for _, l := range fromNet {
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
	defer p.Store.Hold()()
	h.Progress(0, total, "fetching")
	for _, l := range landings {
		names := make([]string, 0, len(l.holders))
		for _, from := range l.holders {
			names = append(names, from.Name)
		}
		if l.slot != "" {
			h.Logf("%s: pulling %s %s from %s", l.slot, l.stored.GetRepo(), l.stored.GetGroup(), strings.Join(names, ", "))
		} else {
			h.Logf("pulling %s %s from %s", l.stored.GetRepo(), l.stored.GetGroup(), strings.Join(names, ", "))
		}
		if err := p.landMesh(ctx, h, l); err != nil {
			return fmt.Errorf("%s %s: %w", l.stored.GetRepo(), l.stored.GetGroup(), err)
		}
	}
	for _, l := range fromNet {
		if l.slot != "" {
			h.Logf("%s: pulling %s %s from %s", l.slot, l.model.GetRepo(), l.g.Name, l.model.GetSourceId())
		}
		if err := p.land(ctx, h, l); err != nil {
			return err
		}
	}
	h.Progress(total, total, "done")
	return nil
}

// Lands one group from members: every blob from the first member serving it, the internet when
// none does, then the manifest copied with its descriptor and the local usage time kept
func (p *Puller) landMesh(ctx context.Context, h *tasks.Handle, l *meshLanding) error {
	stored := l.stored
	key := store.Key(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup())
	unlock := p.Store.Lock(key)
	defer unlock()
	out := proto.Clone(stored).(*v1.StoredModel)
	out.Artifacts = nil
	out.Bytes = 0
	for _, sa := range l.artifacts() {
		if err := ctx.Err(); err != nil {
			return err
		}
		a, digest := sa.GetArtifact(), sa.GetDigest()
		rel := strings.TrimPrefix(sa.GetPath(), strings.TrimRight(stored.GetPath(), "/")+"/")
		if rel == sa.GetPath() {
			rel = path.Base(sa.GetPath())
		}
		if err := p.landBlob(ctx, h, l, sa); err != nil {
			return fmt.Errorf("%s: %w", a.GetPath(), err)
		}
		linked, err := p.Store.Link(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup(), rel, digest)
		if err != nil {
			return err
		}
		out.Artifacts = append(out.Artifacts, &v1.StoredArtifact{Artifact: a, Digest: digest, Path: linked})
		out.Bytes += a.GetSizeBytes()
	}
	switch {
	case l.files != nil && l.slot != "":
		// A part landed as selected files is a component, such as a tokenizer
		out.Descriptor_ = &v1.Descriptor{FormatId: stored.GetFormatId(), Group: stored.GetGroup(), Architecture: strings.TrimPrefix(l.slot, "text_encoder."), Kind: v1.ModelKind_MODEL_KIND_COMPONENT}
		out.Runtimes = 0
	default:
		if out.GetDescriptor_() == nil && l.descriptor != nil {
			out.Descriptor_ = l.descriptor
		}
		out.Runtimes = 0
		if d := out.GetDescriptor_(); d != nil {
			out.Runtimes = p.Inspector.Runtimes.Mask(out.GetFormatId(), d.GetKind())
		}
	}
	dir, err := p.Store.GroupDir(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup())
	if err != nil {
		return err
	}
	out.Path = dir
	out.PulledAt = timestamppb.Now()
	out.UsedAt = nil
	if local, err := p.Store.ReadManifest(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup()); err == nil {
		out.UsedAt = local.GetUsedAt()
	}
	if err := p.Store.WriteManifest(out); err != nil {
		return err
	}
	if err := p.Store.PruneLinks(out); err != nil {
		return err
	}
	p.Events.Publish(v1.EventKind_EVENT_KIND_MODEL, v1.EventAction_EVENT_ACTION_CREATED, key, out)
	p.AnnounceStore()
	return nil
}

// Lands one blob: from the members holding the group in link order, the first that serves it,
// and from the internet when none does and the source answers. The partial is keyed by digest,
// so a transfer interrupted on one path resumes on whichever picks it up, and the fetcher's
// workers, chunks, and digest check are the same on both. The task log says where it came from.
func (p *Puller) landBlob(ctx context.Context, h *tasks.Handle, l *meshLanding, sa *v1.StoredArtifact) error {
	a, digest := sa.GetArtifact(), sa.GetDigest()
	size := int64(a.GetSizeBytes())
	unlock := p.Store.Lock("blob:" + digest)
	if p.Store.HasBlob(digest) {
		unlock()
		h.Add(size)
		h.Logf("%s already stored", a.GetPath())
		return nil
	}
	// Transfers between members are not metered by the schedule or held by its windows.
	mctx := transfer.WithoutSchedule(ctx)
	partial := p.Store.PartialPath(blobKey(digest))
	var refused []string
	for _, from := range l.holders {
		client, err := sources.NewHTTPWith(from.Base, from.HTTP)
		if err != nil {
			unlock()
			return err
		}
		url := from.Base + "/blobs/" + digest
		head, err := client.Do(mctx, http.MethodHead, url, nil, nil)
		if err != nil {
			if ctx.Err() != nil {
				unlock()
				return ctx.Err()
			}
			h.Logf("%s: %s does not serve it: %v", a.GetPath(), from.Name, err)
			refused = append(refused, from.Name)
			continue
		}
		head.Body.Close()
		h.Message("fetching " + a.GetPath() + " from " + from.Name)
		var added atomic.Int64
		hexDigest, err := p.Fetcher.Fetch(mctx, sources.NewRangeBlob(client, url, size), partial, store.Hex(digest), func(d int64) {
			added.Add(d)
			h.Add(d)
		})
		if err != nil {
			h.Add(-added.Load())
			if ctx.Err() != nil {
				unlock()
				return ctx.Err()
			}
			h.Logf("%s from %s: %v", a.GetPath(), from.Name, err)
			refused = append(refused, from.Name)
			continue
		}
		if err := p.Store.Commit(partial, store.Digest(hexDigest)); err != nil {
			unlock()
			return err
		}
		unlock()
		h.Logf("%s verified %s from %s", a.GetPath(), digest, from.Name)
		return nil
	}
	unlock()
	none := "no member served it"
	if len(refused) > 0 {
		none += " (" + strings.Join(refused, ", ") + ")"
	}
	if l.net == nil {
		return fmt.Errorf("%s and %s did not answer", none, l.stored.GetSourceId())
	}
	source := l.net.model.GetSourceId()
	want := artifactAt(l.net, a.GetPath())
	if want == nil {
		return fmt.Errorf("%s and %s no longer lists %s", none, source, a.GetPath())
	}
	h.Logf("%s: %s, fetching from %s", a.GetPath(), none, source)
	got, err := p.fetch(ctx, h, l.net.src, l.net.model, want)
	if err != nil {
		return err
	}
	if got != digest {
		return fmt.Errorf("%s at %s is %s, members hold %s", a.GetPath(), source, got, digest)
	}
	return nil
}

// The source's artifact at a path within a landing, nil when the source no longer lists it
func artifactAt(l *landing, p string) *v1.Artifact {
	for _, a := range l.artifacts() {
		if a.GetPath() == p {
			return a
		}
	}
	return nil
}

// The partial file's key for a blob, one per digest on every path that lands it
func blobKey(digest string) string {
	return "sha256-" + store.Hex(digest)
}
