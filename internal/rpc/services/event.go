package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

var _ nebuv1connect.EventServiceHandler = (*EventService)(nil)

// Produces the current state as events for a fresh subscriber
type Snapshotter func(ctx context.Context, kinds []v1.EventKind) []*v1.Event

// Streams live updates
type EventService struct {
	bus      *events.Bus
	snapshot Snapshotter
}

// Builds the event service
func NewEventService(bus *events.Bus, snapshot Snapshotter) *EventService {
	return &EventService{bus: bus, snapshot: snapshot}
}

func (s *EventService) WatchEvents(ctx context.Context, req *connect.Request[v1.WatchEventsRequest], stream *connect.ServerStream[v1.WatchEventsResponse]) error {
	sub := s.bus.Subscribe(ctx, req.Msg.GetKinds())
	if req.Msg.GetSnapshot() && s.snapshot != nil {
		for _, ev := range s.snapshot(ctx, req.Msg.GetKinds()) {
			if err := stream.Send(&v1.WatchEventsResponse{Event: ev}); err != nil {
				return err
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-sub.Events():
			if err := stream.Send(&v1.WatchEventsResponse{Event: ev}); err != nil {
				return err
			}
		}
	}
}
