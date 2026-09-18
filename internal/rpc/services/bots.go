package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/bots"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

var _ nebuv1connect.BotServiceHandler = (*BotService)(nil)

// Serves Discord bots
type BotService struct {
	bots *bots.Manager
}

func NewBotService(m *bots.Manager) *BotService {
	return &BotService{bots: m}
}

func (s *BotService) ListBots(ctx context.Context, req *connect.Request[v1.ListBotsRequest]) (*connect.Response[v1.ListBotsResponse], error) {
	return reply(&v1.ListBotsResponse{Bots: s.bots.List()}, nil)
}

func (s *BotService) GetBot(ctx context.Context, req *connect.Request[v1.GetBotRequest]) (*connect.Response[v1.GetBotResponse], error) {
	b, err := s.bots.Get(req.Msg.GetId())
	return reply(&v1.GetBotResponse{Bot: b}, err)
}

func (s *BotService) CreateBot(ctx context.Context, req *connect.Request[v1.CreateBotRequest]) (*connect.Response[v1.CreateBotResponse], error) {
	b, err := s.bots.Create(ctx, req.Msg)
	return reply(&v1.CreateBotResponse{Bot: b}, err)
}

func (s *BotService) UpdateBot(ctx context.Context, req *connect.Request[v1.UpdateBotRequest]) (*connect.Response[v1.UpdateBotResponse], error) {
	b, err := s.bots.Update(ctx, req.Msg)
	return reply(&v1.UpdateBotResponse{Bot: b}, err)
}

func (s *BotService) DeleteBot(ctx context.Context, req *connect.Request[v1.DeleteBotRequest]) (*connect.Response[v1.DeleteBotResponse], error) {
	b, err := s.bots.Delete(ctx, req.Msg.GetId())
	return reply(&v1.DeleteBotResponse{Bot: b}, err)
}

func (s *BotService) StartBot(ctx context.Context, req *connect.Request[v1.StartBotRequest]) (*connect.Response[v1.StartBotResponse], error) {
	b, err := s.bots.Start(ctx, req.Msg.GetId())
	return reply(&v1.StartBotResponse{Bot: b}, err)
}

func (s *BotService) StopBot(ctx context.Context, req *connect.Request[v1.StopBotRequest]) (*connect.Response[v1.StopBotResponse], error) {
	b, err := s.bots.Stop(ctx, req.Msg.GetId())
	return reply(&v1.StopBotResponse{Bot: b}, err)
}

func (s *BotService) ListBotActivity(ctx context.Context, req *connect.Request[v1.ListBotActivityRequest]) (*connect.Response[v1.ListBotActivityResponse], error) {
	list, err := s.bots.Activity(req.Msg.GetBotId(), int(req.Msg.GetLimit()))
	return reply(&v1.ListBotActivityResponse{Activity: list}, err)
}

func (s *BotService) ListBotGuilds(ctx context.Context, req *connect.Request[v1.ListBotGuildsRequest]) (*connect.Response[v1.ListBotGuildsResponse], error) {
	list, err := s.bots.Guilds(ctx, req.Msg.GetBotId())
	return reply(&v1.ListBotGuildsResponse{Guilds: list}, err)
}

func (s *BotService) ProbeBotToken(ctx context.Context, req *connect.Request[v1.ProbeBotTokenRequest]) (*connect.Response[v1.ProbeBotTokenResponse], error) {
	return reply(s.bots.Probe(ctx, req.Msg))
}

func (s *BotService) SendBotMessage(ctx context.Context, req *connect.Request[v1.SendBotMessageRequest]) (*connect.Response[v1.SendBotMessageResponse], error) {
	return reply(s.bots.Send(ctx, req.Msg))
}
