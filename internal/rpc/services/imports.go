package services

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/pkg/events"
	nebuv1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"google.golang.org/protobuf/proto"
)

var _ nebuv1connect.PoolImportServiceHandler = (*PoolImportService)(nil)

// The pools a node imports, as the mesh manager tracks them
type Imports interface {
	// Every import by pool id
	Imports() []*nebuv1.PoolImport
	// One import by pool id, false when no member exports it
	Import(id string) (*nebuv1.PoolImport, bool)
}

var _ Imports = (*mesh.Manager)(nil)

// Serves the pools this node imports from other members of its mesh
type PoolImportService struct {
	mesh Imports
	bus  *events.Bus
}

func NewPoolImportService(m Imports, bus *events.Bus) *PoolImportService {
	return &PoolImportService{mesh: m, bus: bus}
}

func (s *PoolImportService) ListPoolImports(ctx context.Context, req *connect.Request[nebuv1.ListPoolImportsRequest]) (*connect.Response[nebuv1.ListPoolImportsResponse], error) {
	return reply(&nebuv1.ListPoolImportsResponse{Imports: s.mesh.Imports()}, nil)
}

func (s *PoolImportService) GetPoolImport(ctx context.Context, req *connect.Request[nebuv1.GetPoolImportRequest]) (*connect.Response[nebuv1.GetPoolImportResponse], error) {
	imp, ok := s.mesh.Import(req.Msg.GetId())
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no member exports pool %q to this node", req.Msg.GetId()))
	}
	return reply(&nebuv1.GetPoolImportResponse{Import: imp}, nil)
}

// Sends every import, then each import whose record changes and each one withdrawn, as pool and node events arrive
func (s *PoolImportService) WatchPoolImports(ctx context.Context, req *connect.Request[nebuv1.WatchPoolImportsRequest], stream *connect.ServerStream[nebuv1.WatchPoolImportsResponse]) error {
	sub := s.bus.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_POOL, v1.EventKind_EVENT_KIND_NODE, v1.EventKind_EVENT_KIND_MESH})
	sent := map[string]*nebuv1.PoolImport{}
	diff := func() error {
		now := map[string]*nebuv1.PoolImport{}
		for _, imp := range s.mesh.Imports() {
			now[imp.GetId()] = imp
			if prev, ok := sent[imp.GetId()]; ok && proto.Equal(prev, imp) {
				continue
			}
			if err := stream.Send(&nebuv1.WatchPoolImportsResponse{Import: imp}); err != nil {
				return err
			}
		}
		for id, prev := range sent {
			if _, ok := now[id]; ok {
				continue
			}
			if err := stream.Send(&nebuv1.WatchPoolImportsResponse{Import: prev, Deleted: true}); err != nil {
				return err
			}
		}
		sent = now
		return nil
	}
	if err := diff(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sub.Events():
			if err := diff(); err != nil {
				return err
			}
		}
	}
}
