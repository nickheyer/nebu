package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/doctor"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/settings"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/launch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/text"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	_ nebuv1connect.HostServiceHandler     = (*HostService)(nil)
	_ nebuv1connect.SettingsServiceHandler = (*SettingsService)(nil)
	_ nebuv1connect.SourceServiceHandler   = (*SourceService)(nil)
	_ nebuv1connect.EstimateServiceHandler = (*EstimateService)(nil)
	_ nebuv1connect.StoreServiceHandler    = (*StoreService)(nil)
)

// Serves host profile, doctor, the daemon's own log and host directories
type HostService struct {
	prober *host.Prober
	doctor *doctor.Doctor
	recent *launch.Log
	store  *store.Store
}

func NewHostService(prober *host.Prober, doc *doctor.Doctor, recent *launch.Log, st *store.Store) *HostService {
	return &HostService{prober: prober, doctor: doc, recent: recent, store: st}
}

func (s *HostService) GetProfile(ctx context.Context, req *connect.Request[v1.GetProfileRequest]) (*connect.Response[v1.GetProfileResponse], error) {
	profile, err := s.prober.Profile(ctx, req.Msg.GetRefresh())
	return reply(&v1.GetProfileResponse{Profile: profile}, err)
}

func (s *HostService) Doctor(ctx context.Context, req *connect.Request[v1.DoctorRequest]) (*connect.Response[v1.DoctorResponse], error) {
	task, err := s.doctor.Start(ctx)
	return reply(&v1.DoctorResponse{Task: task}, err)
}

// Streams recent daemon logs and optionally follows new lines until disconnect.
func (s *HostService) Logs(ctx context.Context, req *connect.Request[v1.HostServiceLogsRequest], stream *connect.ServerStream[v1.HostServiceLogsResponse]) error {
	send := func(lines []string) error { return stream.Send(&v1.HostServiceLogsResponse{Lines: lines}) }
	if !req.Msg.GetFollow() {
		lines := s.recent.Tail(int(req.Msg.GetTail()))
		if len(lines) == 0 {
			return nil
		}
		return wrap(send(lines))
	}
	return wrap(s.recent.Follow(ctx, int(req.Msg.GetTail()), send))
}

// Lists a host directory with directories first, the steps down to it and the places a browser jumps to
func (s *HostService) ListDirectory(ctx context.Context, req *connect.Request[v1.ListDirectoryRequest]) (*connect.Response[v1.ListDirectoryResponse], error) {
	return reply(s.listDirectory(req.Msg))
}

func (s *HostService) listDirectory(req *v1.ListDirectoryRequest) (*v1.ListDirectoryResponse, error) {
	path, err := s.browsePath(req.GetPath(), req.GetStart())
	if err != nil && req.GetNearest() {
		path, err = s.startPath(req.GetStart()), nil
	}
	if err != nil {
		return nil, err
	}
	dir, entries, err := readAround(path)
	// A path that cannot be listed gives way to each directory above it, then to the start place
	for err != nil && req.GetNearest() && filepath.Dir(path) != path {
		path = filepath.Dir(path)
		dir, entries, err = readAround(path)
	}
	if err != nil && req.GetNearest() {
		dir, entries, err = readAround(s.startPath(req.GetStart()))
	}
	if err != nil {
		return nil, err
	}
	out := &v1.ListDirectoryResponse{Path: dir, Trail: trail(dir), Places: s.places()}
	if parent := filepath.Dir(dir); parent != dir {
		out.Parent = parent
	}
	for _, e := range entries {
		// Follow symlinks when identifying directories and executables.
		full := filepath.Join(dir, e.Name())
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		out.Entries = append(out.Entries, &v1.DirEntry{Name: e.Name(), Path: full, Dir: info.IsDir(), Executable: !info.IsDir() && executable(info, e.Name()), SizeBytes: uint64(info.Size())})
	}
	sort.SliceStable(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out, nil
}

// The absolute path a request names, a store reference as its group's directory and no path as the start place
func (s *HostService) browsePath(path string, start v1.DirPlaceKind) (string, error) {
	if path == "" {
		return s.startPath(start), nil
	}
	if !strings.HasPrefix(path, runtimes.StoreScheme) {
		return filepath.Abs(path)
	}
	source, repo, group, err := runtimes.ParseStoreRef(path)
	if err != nil {
		return "", err
	}
	dir, err := s.store.GroupDir(source, repo, group)
	if err != nil {
		return "", fmt.Errorf("%w: %v", runtimes.ErrParam, err)
	}
	return dir, nil
}

// The directory a browse with no path opens on, the store's models when asked for and home otherwise
func (s *HostService) startPath(start v1.DirPlaceKind) string {
	if start == v1.DirPlaceKind_DIR_PLACE_KIND_STORE {
		return s.store.ModelsDir()
	}
	return homeDir()
}

// The store's models, home and every volume root, in the order a browser shows them
func (s *HostService) places() []*v1.DirPlace {
	out := []*v1.DirPlace{
		{Kind: v1.DirPlaceKind_DIR_PLACE_KIND_STORE, Path: s.store.ModelsDir()},
		{Kind: v1.DirPlaceKind_DIR_PLACE_KIND_HOME, Path: homeDir()},
	}
	for _, root := range volumes() {
		out = append(out, &v1.DirPlace{Kind: v1.DirPlaceKind_DIR_PLACE_KIND_VOLUME, Path: root})
	}
	return out
}

// The daemon's home directory, the filesystem root when the host names none
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return string(filepath.Separator)
	}
	return home
}

// Every drive root on Windows, the one filesystem root elsewhere
func volumes() []string {
	if runtime.GOOS != "windows" {
		return []string{string(filepath.Separator)}
	}
	var out []string
	for letter := 'A'; letter <= 'Z'; letter++ {
		root := string(letter) + `:\`
		if _, err := os.Stat(root); err == nil {
			out = append(out, root)
		}
	}
	return out
}

// The directory a path names or sits in, with its entries
func readAround(path string) (string, []os.DirEntry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		path = filepath.Dir(path)
	}
	entries, err := os.ReadDir(path)
	return path, entries, err
}

// Every directory from the volume root down to dir, each named by its last element
func trail(dir string) []*v1.DirStep {
	var out []*v1.DirStep
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			out = append(out, &v1.DirStep{Name: dir, Path: dir})
			break
		}
		out = append(out, &v1.DirStep{Name: filepath.Base(dir), Path: dir})
		dir = parent
	}
	slices.Reverse(out)
	return out
}

// Detect executables by extension on Windows and file mode elsewhere.
func executable(info os.FileInfo, name string) bool {
	if runtime.GOOS == "windows" {
		switch strings.ToLower(filepath.Ext(name)) {
		case ".exe", ".bat", ".cmd", ".com":
			return true
		}
		return false
	}
	return info.Mode()&0o111 != 0
}

// Serves host preferences.
type SettingsService struct {
	settings *settings.Manager
	webhooks WebhookTester
}

// Posts a sample notification to a webhook and answers the status it got
type WebhookTester interface {
	Test(ctx context.Context, hook *v1.Webhook) (int, error)
}

func NewSettingsService(m *settings.Manager, webhooks WebhookTester) *SettingsService {
	return &SettingsService{settings: m, webhooks: webhooks}
}

func (s *SettingsService) TestWebhook(ctx context.Context, req *connect.Request[v1.TestWebhookRequest]) (*connect.Response[v1.TestWebhookResponse], error) {
	hook := proto.Clone(req.Msg.GetWebhook()).(*v1.Webhook)
	if err := settings.CheckWebhookTarget(hook); err != nil {
		return reply(&v1.TestWebhookResponse{}, err)
	}
	status, err := s.webhooks.Test(ctx, hook)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return reply(&v1.TestWebhookResponse{Status: uint32(status)}, nil)
}

func (s *SettingsService) GetSettings(ctx context.Context, req *connect.Request[v1.GetSettingsRequest]) (*connect.Response[v1.GetSettingsResponse], error) {
	return reply(&v1.GetSettingsResponse{Settings: s.settings.Get(), Defaults: s.settings.Base()}, nil)
}

func (s *SettingsService) UpdateSettings(ctx context.Context, req *connect.Request[v1.UpdateSettingsRequest]) (*connect.Response[v1.UpdateSettingsResponse], error) {
	out, err := s.settings.Update(ctx, req.Msg.GetSettings())
	return reply(&v1.UpdateSettingsResponse{Settings: out}, err)
}

// Serves catalog lookups and source settings.
type SourceService struct {
	sources   *sources.Manager
	inspector *inspect.Inspector
	formats   *formats.Registry
	runtimes  *runtimes.Registry
}

// Provider pages fetched for one page of results when providers cannot apply every filter.
const searchRounds = 8

// Creates the source service with format and runtime registries.
func NewSourceService(m *sources.Manager, insp *inspect.Inspector, fmts *formats.Registry, reg *runtimes.Registry) *SourceService {
	return &SourceService{sources: m, inspector: insp, formats: fmts, runtimes: reg}
}

// The format ids in priority order
func (s *SourceService) formatIDs() []string {
	var out []string
	for _, f := range s.formats.List() {
		out = append(out, f.ID())
	}
	return out
}

// Shared runtime and format facets.
func (s *SourceService) sharedFacets() []*v1.Facet {
	runtime := &v1.Facet{Id: sources.FacetRuntime, Label: "Runtime"}
	for _, rt := range s.runtimes.List() {
		runtime.Values = append(runtime.Values, &v1.FacetValue{Id: rt.ID(), Label: rt.Name()})
	}
	format := &v1.Facet{Id: sources.FacetFormat, Label: "Format", Multi: true}
	for _, f := range s.formats.List() {
		format.Values = append(format.Values, &v1.FacetValue{Id: f.ID(), Label: f.Description()})
	}
	return []*v1.Facet{runtime, format}
}

// Lists sources with shared facets before provider-specific facets.
func (s *SourceService) ListSources(ctx context.Context, req *connect.Request[v1.ListSourcesRequest]) (*connect.Response[v1.ListSourcesResponse], error) {
	statuses := s.sources.Registry.Statuses(ctx)
	for _, st := range statuses {
		if st.GetCapabilities() != nil {
			st.Capabilities.Facets = append(s.sharedFacets(), st.Capabilities.Facets...)
		}
	}
	statuses = match(statuses, req.Msg.GetQuery(), func(st *v1.SourceStatus) []string {
		return []string{st.GetSource().GetId(), st.GetSource().GetName(), text.Enum(st.GetSource().GetKind()), st.GetCapabilities().GetName()}
	})
	statuses, next, err := page(statuses, req.Msg.GetCursor(), req.Msg.GetLimit())
	return reply(&v1.ListSourcesResponse{Sources: statuses, NextCursor: next}, err)
}

func (s *SourceService) ListProviders(ctx context.Context, req *connect.Request[v1.ListProvidersRequest]) (*connect.Response[v1.ListProvidersResponse], error) {
	return reply(&v1.ListProvidersResponse{Providers: sources.Providers()}, nil)
}

// Writes source settings, rebuilds the client, and returns status.
func (s *SourceService) write(ctx context.Context, write func(context.Context, *v1.Source) (*v1.Source, error), in *v1.Source) (*v1.SourceStatus, error) {
	row, err := write(ctx, in)
	if err != nil {
		return nil, err
	}
	return s.sources.Registry.Status(ctx, row.GetId())
}

func (s *SourceService) CreateSource(ctx context.Context, req *connect.Request[v1.CreateSourceRequest]) (*connect.Response[v1.CreateSourceResponse], error) {
	st, err := s.write(ctx, s.sources.Create, req.Msg.GetSource())
	return reply(&v1.CreateSourceResponse{Source: st}, err)
}

func (s *SourceService) UpdateSource(ctx context.Context, req *connect.Request[v1.UpdateSourceRequest]) (*connect.Response[v1.UpdateSourceResponse], error) {
	st, err := s.write(ctx, s.sources.Update, req.Msg.GetSource())
	return reply(&v1.UpdateSourceResponse{Source: st}, err)
}

func (s *SourceService) DeleteSource(ctx context.Context, req *connect.Request[v1.DeleteSourceRequest]) (*connect.Response[v1.DeleteSourceResponse], error) {
	row, err := s.sources.Delete(ctx, req.Msg.GetId())
	return reply(&v1.DeleteSourceResponse{Source: row}, err)
}

// The compatibility bits, formats and model kinds a search asks for beyond what providers filter
type wanted struct {
	mask    uint32
	formats []string
	kinds   []v1.ModelKind
	// Set when the runtimes serve none of the formats or kinds asked for
	nothing bool
}

// Turns the runtime facet into the formats and kinds its runtimes serve, for providers to filter by
func (s *SourceService) wanted(in *v1.SearchRequest) (wanted, error) {
	var w wanted
	w.formats = sources.Filter(in, sources.FacetFormat)
	for _, f := range w.formats {
		if s.formats.Get(f) == nil {
			return w, fmt.Errorf("%w: no format %q, one of %s", runtimes.ErrParam, f, strings.Join(s.formatIDs(), ", "))
		}
	}
	kinds, err := sources.Kinds(in)
	if err != nil {
		return w, err
	}
	w.kinds = kinds
	ids := sources.Filter(in, sources.FacetRuntime)
	if len(ids) == 0 {
		return w, nil
	}
	var served []string
	var serves []v1.ModelKind
	for _, id := range ids {
		rt, err := s.runtimes.Get(id)
		if err != nil {
			return w, err
		}
		w.mask |= s.runtimes.Bit(id)
		for _, f := range rt.Formats() {
			if !slices.Contains(served, f) {
				served = append(served, f)
			}
		}
		if !slices.Contains(serves, rt.Kind()) {
			serves = append(serves, rt.Kind())
		}
	}
	if len(w.formats) == 0 {
		w.formats = served
	} else {
		w.formats = slices.DeleteFunc(slices.Clone(w.formats), func(f string) bool { return !slices.Contains(served, f) })
	}
	if len(w.kinds) == 0 {
		w.kinds = serves
	} else {
		w.kinds = slices.DeleteFunc(slices.Clone(w.kinds), func(k v1.ModelKind) bool { return !slices.Contains(serves, k) })
	}
	w.nothing = len(w.formats) == 0 || len(w.kinds) == 0
	names := make([]string, len(w.kinds))
	for i, k := range w.kinds {
		names[i] = sources.KindName(k)
	}
	delete(in.Filters, sources.FacetRuntime)
	in.Filters[sources.FacetFormat] = strings.Join(w.formats, ",")
	in.Filters[sources.FacetKind] = strings.Join(names, ",")
	return w, nil
}

// Reports whether a stamped hit is what the search asked for.
func (w wanted) admits(h *v1.SearchHit) bool {
	if w.mask != 0 && h.GetRuntimes()&w.mask == 0 {
		return false
	}
	if len(w.formats) > 0 && !holdsAny(h.GetFormats(), w.formats) {
		return false
	}
	if len(w.kinds) > 0 && !slices.Contains(w.kinds, h.GetKind()) {
		return false
	}
	return true
}

// Searches selected sources and labels hits with model kind, formats, and runtimes. Providers
// apply the format and kind filters their APIs offer. Hits they still list that miss the filters
// are dropped, and further pages are fetched until the page holds the requested number of hits
// or the sources run out.
func (s *SourceService) Search(ctx context.Context, req *connect.Request[v1.SearchRequest]) (*connect.Response[v1.SearchResponse], error) {
	in := proto.Clone(req.Msg).(*v1.SearchRequest)
	if in.Filters == nil {
		in.Filters = map[string]string{}
	}
	w, err := s.wanted(in)
	if err != nil {
		return nil, wrap(err)
	}
	out := &v1.SearchResponse{}
	if w.nothing {
		return connect.NewResponse(out), nil
	}
	limit := int(in.GetLimit())
	if limit <= 0 {
		limit = sources.DefaultLimit
	}
	seen := map[string]bool{}
	warned := map[string]bool{}
	dropped := false
	for round := 0; round < searchRounds; round++ {
		resp, err := s.sources.Registry.Search(ctx, in)
		if err != nil {
			return nil, wrap(err)
		}
		for _, warning := range resp.GetWarnings() {
			if !warned[warning] {
				warned[warning] = true
				out.Warnings = append(out.Warnings, warning)
			}
		}
		if round == 0 {
			out.Total = resp.GetTotal()
		}
		out.NextCursor = resp.GetNextCursor()
		for _, h := range resp.GetHits() {
			key := h.GetSourceId() + "/" + h.GetRepo()
			if seen[key] {
				continue
			}
			seen[key] = true
			s.stamp(h)
			if !w.admits(h) {
				dropped = true
				continue
			}
			out.Hits = append(out.Hits, h)
		}
		if len(out.Hits) >= limit || out.NextCursor == "" {
			break
		}
		in.Cursor = out.NextCursor
	}
	// Provider counts include the hits dropped here.
	if dropped {
		out.Total = 0
	}
	return connect.NewResponse(out), nil
}

// Labels a hit with model kind, formats, and compatible runtimes.
func (s *SourceService) stamp(h *v1.SearchHit) {
	h.Kind = sources.Kind(h)
	h.Formats = sources.Formats(h, s.formatIDs(), h.GetKind())
	h.Runtimes = s.runtimes.MaskOf(h.GetFormats(), h.GetKind())
}

func holdsAny(have, want []string) bool {
	for _, w := range want {
		for _, h := range have {
			if h == w {
				return true
			}
		}
	}
	return false
}

func (s *SourceService) Resolve(ctx context.Context, req *connect.Request[v1.ResolveRequest]) (*connect.Response[v1.ResolveResponse], error) {
	_, model, err := s.inspector.Resolve(ctx, req.Msg.GetSourceId(), req.Msg.GetRepo(), req.Msg.GetRevision())
	return reply(&v1.ResolveResponse{Model: model}, err)
}

func (s *SourceService) ListRevisions(ctx context.Context, req *connect.Request[v1.ListRevisionsRequest]) (*connect.Response[v1.ListRevisionsResponse], error) {
	src, err := s.sources.Registry.Get(req.Msg.GetSourceId())
	if err != nil {
		return nil, wrap(err)
	}
	revisions, err := src.Revisions(ctx, req.Msg.GetRepo())
	return reply(&v1.ListRevisionsResponse{Revisions: revisions}, err)
}

func (s *SourceService) GetModelCard(ctx context.Context, req *connect.Request[v1.GetModelCardRequest]) (*connect.Response[v1.GetModelCardResponse], error) {
	src, err := s.sources.Registry.Get(req.Msg.GetSourceId())
	if err != nil {
		return nil, wrap(err)
	}
	card, err := src.Card(ctx, req.Msg.GetRepo(), req.Msg.GetRevision())
	if err == nil && card.GetHit() != nil {
		s.stamp(card.Hit)
	}
	return reply(&v1.GetModelCardResponse{Card: card}, err)
}

// Serves inspection and planning
type EstimateService struct {
	inspector *inspect.Inspector
}

func NewEstimateService(insp *inspect.Inspector) *EstimateService {
	return &EstimateService{inspector: insp}
}

func (s *EstimateService) Inspect(ctx context.Context, req *connect.Request[v1.InspectRequest]) (*connect.Response[v1.InspectResponse], error) {
	return reply(s.inspector.Inspect(ctx, req.Msg))
}

func (s *EstimateService) Estimate(ctx context.Context, req *connect.Request[v1.EstimateRequest]) (*connect.Response[v1.EstimateResponse], error) {
	return reply(s.inspector.Estimate(ctx, req.Msg))
}

// Lists resolved blueprint parts for a group.
func (s *EstimateService) Parts(ctx context.Context, req *connect.Request[v1.PartsRequest]) (*connect.Response[v1.PartsResponse], error) {
	plan, err := s.inspector.PartsOf(ctx, req.Msg.GetSourceId(), req.Msg.GetRepo(), req.Msg.GetRevision(), req.Msg.GetGroup())
	if err != nil {
		return nil, wrap(err)
	}
	return reply(plan.Proto(), nil)
}

// Serves the model store
type StoreService struct {
	pullTo   PullTo
	store    *store.Store
	puller   *pull.Puller
	events   *events.Bus
	runtimes *runtimes.Registry
}

// Pulls a model onto another member, following that member's pull in one task here
type PullTo func(ctx context.Context, nodeID string, req *v1.PullRequest) (*v1.Task, error)

// Builds the store service, the runtime registry naming the compatibility bit a runtime filter reads
func NewStoreService(st *store.Store, p *pull.Puller, bus *events.Bus, to PullTo, reg *runtimes.Registry) *StoreService {
	return &StoreService{pullTo: to, store: st, puller: p, events: bus, runtimes: reg}
}

func (s *StoreService) Pull(ctx context.Context, req *connect.Request[v1.PullRequest]) (*connect.Response[v1.PullResponse], error) {
	if req.Msg.GetNodeId() != "" {
		task, err := s.pullTo(ctx, req.Msg.GetNodeId(), req.Msg)
		return reply(&v1.PullResponse{Task: task}, err)
	}
	task, err := s.puller.Pull(ctx, req.Msg)
	return reply(&v1.PullResponse{Task: task}, err)
}

// Lists stored models matching every filter and the query, in the order asked, a page at a time
func (s *StoreService) ListModels(ctx context.Context, req *connect.Request[v1.ListModelsRequest]) (*connect.Response[v1.ListModelsResponse], error) {
	models, err := s.store.ListManifests()
	if err != nil {
		return nil, wrap(err)
	}
	if kinds := req.Msg.GetKinds(); len(kinds) > 0 {
		models = keep(models, func(m *v1.StoredModel) bool { return slices.Contains(kinds, m.GetDescriptor_().GetKind()) })
	}
	if ids := req.Msg.GetRuntimeIds(); len(ids) > 0 {
		var mask uint32
		for _, id := range ids {
			if _, err := s.runtimes.Get(id); err != nil {
				return nil, wrap(err)
			}
			mask |= s.runtimes.Bit(id)
		}
		models = keep(models, func(m *v1.StoredModel) bool { return m.GetRuntimes()&mask != 0 })
	}
	if formats := req.Msg.GetFormatIds(); len(formats) > 0 {
		models = keep(models, func(m *v1.StoredModel) bool { return slices.Contains(formats, m.GetFormatId()) })
	}
	models = match(models, req.Msg.GetQuery(), func(m *v1.StoredModel) []string {
		return []string{m.GetRepo(), m.GetGroup(), m.GetFormatId(), m.GetDescriptor_().GetArchitecture(), m.GetDescriptor_().GetPrecision().GetLabel()}
	})
	sortModels(models, req.Msg.GetSort())
	if req.Msg.GetByRepo() {
		gatherRepos(models)
	}
	models, next, err := page(models, req.Msg.GetCursor(), req.Msg.GetLimit())
	return reply(&v1.ListModelsResponse{Models: models, NextCursor: next}, err)
}

// Orders models as a listing asks, unset times last, the store's own key order otherwise
func sortModels(models []*v1.StoredModel, by v1.ModelSort) {
	stamp := func(ts *timestamppb.Timestamp) int64 {
		if ts == nil {
			return -1
		}
		return ts.AsTime().UnixNano()
	}
	switch by {
	case v1.ModelSort_MODEL_SORT_LAST_USED:
		sort.SliceStable(models, func(i, j int) bool { return stamp(models[i].GetUsedAt()) > stamp(models[j].GetUsedAt()) })
	case v1.ModelSort_MODEL_SORT_PULLED:
		sort.SliceStable(models, func(i, j int) bool { return stamp(models[i].GetPulledAt()) > stamp(models[j].GetPulledAt()) })
	case v1.ModelSort_MODEL_SORT_SIZE:
		sort.SliceStable(models, func(i, j int) bool { return models[i].GetBytes() > models[j].GetBytes() })
	}
}

// Moves every model up beside the first of its repository, keeping the order inside each repository
func gatherRepos(models []*v1.StoredModel) {
	first := map[[2]string]int{}
	for i, m := range models {
		key := [2]string{m.GetSourceId(), m.GetRepo()}
		if _, seen := first[key]; !seen {
			first[key] = i
		}
	}
	sort.SliceStable(models, func(i, j int) bool {
		return first[[2]string{models[i].GetSourceId(), models[i].GetRepo()}] < first[[2]string{models[j].GetSourceId(), models[j].GetRepo()}]
	})
}

func (s *StoreService) GetModel(ctx context.Context, req *connect.Request[v1.GetModelRequest]) (*connect.Response[v1.GetModelResponse], error) {
	m, err := s.store.ReadManifest(req.Msg.GetSourceId(), req.Msg.GetRepo(), req.Msg.GetGroup())
	return reply(&v1.GetModelResponse{Model: m}, err)
}

func (s *StoreService) RemoveModel(ctx context.Context, req *connect.Request[v1.RemoveModelRequest]) (*connect.Response[v1.RemoveModelResponse], error) {
	// Wait for concurrent pulls or launches of this group.
	unlock := s.store.Lock(store.Key(req.Msg.GetSourceId(), req.Msg.GetRepo(), req.Msg.GetGroup()))
	m, err := s.store.RemoveManifest(req.Msg.GetSourceId(), req.Msg.GetRepo(), req.Msg.GetGroup())
	unlock()
	if err != nil {
		return nil, wrap(err)
	}
	defer s.puller.AnnounceStore()
	s.events.Publish(v1.EventKind_EVENT_KIND_MODEL, v1.EventAction_EVENT_ACTION_DELETED, store.Key(m.GetSourceId(), m.GetRepo(), m.GetGroup()), m)
	resp := &v1.RemoveModelResponse{Model: m}
	if req.Msg.GetGc() {
		resp.Gc, err = s.store.Gc(false)
	}
	return reply(resp, err)
}

func (s *StoreService) Gc(ctx context.Context, req *connect.Request[v1.GcRequest]) (*connect.Response[v1.GcResponse], error) {
	resp, err := s.store.Gc(req.Msg.GetPartials())
	s.puller.AnnounceStore()
	return reply(resp, err)
}

func (s *StoreService) Verify(ctx context.Context, req *connect.Request[v1.VerifyRequest]) (*connect.Response[v1.VerifyResponse], error) {
	task, err := s.puller.Verify(ctx, req.Msg)
	return reply(&v1.VerifyResponse{Task: task}, err)
}

func (s *StoreService) Export(ctx context.Context, req *connect.Request[v1.ExportRequest]) (*connect.Response[v1.ExportResponse], error) {
	task, err := s.puller.Export(ctx, req.Msg)
	return reply(&v1.ExportResponse{Task: task}, err)
}

func (s *StoreService) GetStatus(ctx context.Context, req *connect.Request[v1.GetStatusRequest]) (*connect.Response[v1.GetStatusResponse], error) {
	st, err := s.store.Status()
	return reply(&v1.GetStatusResponse{Status: st}, err)
}
