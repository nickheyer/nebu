package services

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/chats"
	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

var _ nebuv1connect.ChatServiceHandler = (*ChatService)(nil)

// Serves each account's saved conversations
type ChatService struct {
	chats *chats.Manager
	guard *auth.Guard
}

func NewChatService(m *chats.Manager, guard *auth.Guard) *ChatService {
	return &ChatService{chats: m, guard: guard}
}

// The store behind the request's credential
func (s *ChatService) owner(h http.Header) db.ChatOwner {
	sess, _ := s.guard.Identify(h)
	return chats.OwnerOf(sess)
}

func (s *ChatService) ListConversations(ctx context.Context, req *connect.Request[v1.ListConversationsRequest]) (*connect.Response[v1.ListConversationsResponse], error) {
	list, err := s.chats.List(ctx, s.owner(req.Header()))
	return reply(&v1.ListConversationsResponse{Conversations: list}, err)
}

func (s *ChatService) GetConversation(ctx context.Context, req *connect.Request[v1.GetConversationRequest]) (*connect.Response[v1.GetConversationResponse], error) {
	c, err := s.chats.Get(ctx, s.owner(req.Header()), req.Msg.GetId())
	return reply(&v1.GetConversationResponse{Conversation: c}, err)
}

func (s *ChatService) PutConversation(ctx context.Context, req *connect.Request[v1.PutConversationRequest]) (*connect.Response[v1.PutConversationResponse], error) {
	c, err := s.chats.Put(ctx, s.owner(req.Header()), req.Msg.GetConversation())
	return reply(&v1.PutConversationResponse{Conversation: c}, err)
}

func (s *ChatService) RenameConversation(ctx context.Context, req *connect.Request[v1.RenameConversationRequest]) (*connect.Response[v1.RenameConversationResponse], error) {
	c, err := s.chats.Rename(ctx, s.owner(req.Header()), req.Msg.GetId(), req.Msg.GetTitle())
	return reply(&v1.RenameConversationResponse{Conversation: c}, err)
}

func (s *ChatService) DeleteConversation(ctx context.Context, req *connect.Request[v1.DeleteConversationRequest]) (*connect.Response[v1.DeleteConversationResponse], error) {
	return reply(&v1.DeleteConversationResponse{}, s.chats.Delete(ctx, s.owner(req.Header()), req.Msg.GetId()))
}

func (s *ChatService) PutChatFile(ctx context.Context, req *connect.Request[v1.PutChatFileRequest]) (*connect.Response[v1.PutChatFileResponse], error) {
	f, err := s.chats.PutFile(ctx, s.owner(req.Header()), req.Msg.GetFile(), req.Msg.GetData())
	return reply(&v1.PutChatFileResponse{File: f}, err)
}

func (s *ChatService) GetChatFile(ctx context.Context, req *connect.Request[v1.GetChatFileRequest]) (*connect.Response[v1.GetChatFileResponse], error) {
	f, data, err := s.chats.GetFile(ctx, s.owner(req.Header()), req.Msg.GetId())
	return reply(&v1.GetChatFileResponse{File: f, Data: data}, err)
}

func (s *ChatService) DeleteChatFiles(ctx context.Context, req *connect.Request[v1.DeleteChatFilesRequest]) (*connect.Response[v1.DeleteChatFilesResponse], error) {
	return reply(&v1.DeleteChatFilesResponse{}, s.chats.DeleteFiles(ctx, s.owner(req.Header()), req.Msg.GetIds()))
}
