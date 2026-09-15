package services

import (
	"context"
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
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
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

// Streams the daemon's own log: the last tail lines, then when following every line after until the
// client goes
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

// Serves host wide preferences
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

// Serves catalog lookups and the source rows behind them
type SourceService struct {
	sources   *sources.Manager
	inspector *inspect.Inspector
	formats   []string
}

// Builds the source service, formats are what hits get tagged with, in priority order
func NewSourceService(m *sources.Manager, insp *inspect.Inspector, fmts *formats.Registry) *SourceService {
	s := &SourceService{sources: m, inspector: insp}
	for _, f := range fmts.List() {
		s.formats = append(s.formats, f.ID())
	}
	return s
}

func (s *SourceService) ListSources(ctx context.Context, req *connect.Request[v1.ListSourcesRequest]) (*connect.Response[v1.ListSourcesResponse], error) {
	return reply(&v1.ListSourcesResponse{Sources: s.sources.Registry.Statuses(ctx)}, nil)
}

func (s *SourceService) ListProviders(ctx context.Context, req *connect.Request[v1.ListProvidersRequest]) (*connect.Response[v1.ListProvidersResponse], error) {
	return reply(&v1.ListProvidersResponse{Providers: sources.Providers()}, nil)
}

// Writes a source through write and answers with its status, the client rebuilt
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

// Searches one source, every source of a provider, or every source there is
func (s *SourceService) Search(ctx context.Context, req *connect.Request[v1.SearchRequest]) (*connect.Response[v1.SearchResponse], error) {
	resp, err := s.sources.Registry.Search(ctx, req.Msg)
	if err != nil {
		return nil, wrap(err)
	}
	for _, h := range resp.GetHits() {
		if len(h.Formats) == 0 {
			h.Formats = s.formatsOf(h)
		}
	}
	return connect.NewResponse(resp), nil
}

// Names the formats a hit advertises through its tags
func (s *SourceService) formatsOf(h *v1.SearchHit) []string {
	var out []string
	for _, id := range s.formats {
		for _, t := range append([]string{h.GetLibrary()}, h.GetTags()...) {
			if strings.EqualFold(t, id) {
				out = append(out, id)
				break
			}
		}
	}
	return out
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
	// A pull or a launch of the same group holds the key, so the removal waits its turn
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
