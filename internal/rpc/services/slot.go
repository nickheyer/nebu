package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/slots"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

var _ nebuv1connect.SlotServiceHandler = (*SlotService)(nil)

// Serves slots and swaps
type SlotService struct {
	slots *slots.Manager
}

// Builds the slot service
func NewSlotService(m *slots.Manager) *SlotService {
	return &SlotService{slots: m}
}

func (s *SlotService) ListSlots(ctx context.Context, req *connect.Request[v1.ListSlotsRequest]) (*connect.Response[v1.ListSlotsResponse], error) {
	return connect.NewResponse(&v1.ListSlotsResponse{Slots: s.slots.List()}), nil
}

func (s *SlotService) GetSlot(ctx context.Context, req *connect.Request[v1.GetSlotRequest]) (*connect.Response[v1.GetSlotResponse], error) {
	slot, in, err := s.slots.Get(req.Msg.GetId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.GetSlotResponse{Slot: slot, Instance: in}), nil
}

func (s *SlotService) CreateSlot(ctx context.Context, req *connect.Request[v1.CreateSlotRequest]) (*connect.Response[v1.CreateSlotResponse], error) {
	slot, err := s.slots.Create(ctx, req.Msg)
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.CreateSlotResponse{Slot: slot}), nil
}

func (s *SlotService) UpdateSlot(ctx context.Context, req *connect.Request[v1.UpdateSlotRequest]) (*connect.Response[v1.UpdateSlotResponse], error) {
	slot, err := s.slots.Update(ctx, req.Msg)
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.UpdateSlotResponse{Slot: slot}), nil
}

func (s *SlotService) DeleteSlot(ctx context.Context, req *connect.Request[v1.DeleteSlotRequest]) (*connect.Response[v1.DeleteSlotResponse], error) {
	slot, err := s.slots.Delete(ctx, req.Msg.GetId(), req.Msg.GetForce())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.DeleteSlotResponse{Slot: slot}), nil
}

func (s *SlotService) Swap(ctx context.Context, req *connect.Request[v1.SwapRequest]) (*connect.Response[v1.SwapResponse], error) {
	slot, in, task, err := s.slots.Swap(ctx, req.Msg)
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.SwapResponse{Slot: slot, Instance: in, Task: task}), nil
}

func (s *SlotService) EvictSlot(ctx context.Context, req *connect.Request[v1.EvictSlotRequest]) (*connect.Response[v1.EvictSlotResponse], error) {
	slot, err := s.slots.Evict(ctx, req.Msg.GetId())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.EvictSlotResponse{Slot: slot}), nil
}
