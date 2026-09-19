package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	"google.golang.org/protobuf/proto"
)

var (
	_ nebuv1connect.HostServiceHandler     = (*HostService)(nil)
	_ nebuv1connect.SettingsServiceHandler = (*SettingsService)(nil)
	_ nebuv1connect.SourceServiceHandler   = (*SourceService)(nil)
	_ nebuv1connect.EstimateServiceHandler = (*EstimateService)(nil)
	_ nebuv1connect.StoreServiceHandler    = (*StoreService)(nil)
)

// Serves host profile, doctor, and the daemon's own log
type HostService struct {
	prober *host.Prober
	doctor *doctor.Doctor
	recent *launch.Log
}

func NewHostService(prober *host.Prober, doc *doctor.Doctor, recent *launch.Log) *HostService {
	return &HostService{prober: prober, doctor: doc, recent: recent}
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

// Lists host files, sorting directories first and then by name. Defaults to
// the daemon's home directory. File paths select their parent directory.
func (s *HostService) ListDirectory(ctx context.Context, req *connect.Request[v1.ListDirectoryRequest]) (*connect.Response[v1.ListDirectoryResponse], error) {
	return reply(listDirectory(req.Msg.GetPath()))
}

func listDirectory(path string) (*v1.ListDirectoryResponse, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = string(filepath.Separator)
		}
		path = home
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		path = filepath.Dir(path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := &v1.ListDirectoryResponse{Path: path}
	if parent := filepath.Dir(path); parent != path {
		out.Parent = parent
	}
	for _, e := range entries {
		// Follow symlinks when identifying directories and executables.
		info, err := os.Stat(filepath.Join(path, e.Name()))
		if err != nil {
			continue
		}
		out.Entries = append(out.Entries, &v1.DirEntry{Name: e.Name(), Dir: info.IsDir(), Executable: !info.IsDir() && executable(info, e.Name()), SizeBytes: uint64(info.Size())})
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
}

func NewSettingsService(m *settings.Manager) *SettingsService {
	return &SettingsService{settings: m}
}

func (s *SettingsService) GetSettings(ctx context.Context, req *connect.Request[v1.GetSettingsRequest]) (*connect.Response[v1.GetSettingsResponse], error) {
	return reply(&v1.GetSettingsResponse{Settings: s.settings.Get()}, nil)
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

// Maximum pages to search past when filters remove all hits.
const filteredPages = 8

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
	return reply(&v1.ListSourcesResponse{Sources: statuses}, nil)
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

// Searches selected sources and labels hits with model kind, formats, and runtimes.
// Applies runtime and format filters after provider filters. Continues past empty
// filtered pages while preserving the cursor, within the page limit.
func (s *SourceService) Search(ctx context.Context, req *connect.Request[v1.SearchRequest]) (*connect.Response[v1.SearchResponse], error) {
	in := proto.Clone(req.Msg).(*v1.SearchRequest)
	var wantMask uint32
	if id := strings.TrimSpace(in.GetFilters()[sources.FacetRuntime]); id != "" {
		if _, err := s.runtimes.Get(id); err != nil {
			return nil, wrap(err)
		}
		wantMask = s.runtimes.Bit(id)
	}
	wantFormats := sources.Filter(in, sources.FacetFormat)
	for _, f := range wantFormats {
		if s.formats.Get(f) == nil {
			return nil, wrap(fmt.Errorf("%w: no format %q, one of %s", runtimes.ErrParam, f, strings.Join(s.formatIDs(), ", ")))
		}
	}
	out := &v1.SearchResponse{}
	dropped := false
	for page := 0; page < filteredPages; page++ {
		resp, err := s.sources.Registry.Search(ctx, in)
		if err != nil {
			return nil, wrap(err)
		}
		out.Warnings = append(out.Warnings, resp.GetWarnings()...)
		out.NextCursor = resp.GetNextCursor()
		if page == 0 {
			out.Total = resp.GetTotal()
		}
		for _, h := range resp.GetHits() {
			s.stamp(h)
			if wantMask != 0 && h.GetRuntimes()&wantMask == 0 || len(wantFormats) > 0 && !holdsAny(h.GetFormats(), wantFormats) {
				dropped = true
				continue
			}
			out.Hits = append(out.Hits, h)
		}
		if len(out.Hits) > 0 || out.NextCursor == "" {
			break
		}
		in.Cursor = out.NextCursor
	}
	// Provider counts do not reflect local filtering.
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
	store  *store.Store
	puller *pull.Puller
	events *events.Bus
}

func NewStoreService(st *store.Store, p *pull.Puller, bus *events.Bus) *StoreService {
	return &StoreService{store: st, puller: p, events: bus}
}

func (s *StoreService) Pull(ctx context.Context, req *connect.Request[v1.PullRequest]) (*connect.Response[v1.PullResponse], error) {
	task, err := s.puller.Pull(ctx, req.Msg)
	return reply(&v1.PullResponse{Task: task}, err)
}

func (s *StoreService) ListModels(ctx context.Context, req *connect.Request[v1.ListModelsRequest]) (*connect.Response[v1.ListModelsResponse], error) {
	models, err := s.store.ListManifests()
	return reply(&v1.ListModelsResponse{Models: models}, err)
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
