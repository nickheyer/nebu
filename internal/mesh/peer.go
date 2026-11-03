package mesh

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/mesh/links"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

var _ nebuv1connect.MeshPeerServiceHandler = (*peerService)(nil)

// The membership service the mesh listener serves, each call bound to the certificate its connection proved
type peerService struct {
	m *Manager
}

// The caller of a request when it is of the kind wanted, a permission error otherwise
func callerKind(ctx context.Context, kind peerKind) (caller, error) {
	c, ok := callerOf(ctx)
	if !ok || c.kind != kind {
		return caller{}, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%w: this call is not open to the certificate presented", ErrMesh))
	}
	return c, nil
}

// A connect error as the manager coded it, any other coded by its kind
func coded(err error) error {
	var ce *connect.Error
	switch {
	case err == nil, errors.As(err, &ce):
		return err
	case errors.Is(err, ErrNoMesh):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, ErrUnknownNode):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrMesh):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

// Wraps an answer or its error for connect
func answer[T any](msg *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, coded(err)
	}
	return connect.NewResponse(msg), nil
}

func (s *peerService) Enroll(ctx context.Context, req *connect.Request[v1.EnrollRequest]) (*connect.Response[v1.EnrollResponse], error) {
	c, err := callerKind(ctx, peerGrant)
	if err != nil {
		return nil, err
	}
	return answer(s.m.enroll(ctx, c, req.Msg))
}

func (s *peerService) Renew(ctx context.Context, req *connect.Request[v1.RenewRequest]) (*connect.Response[v1.RenewResponse], error) {
	c, err := callerKind(ctx, peerMember)
	if err != nil {
		return nil, err
	}
	return answer(s.m.renewFor(c, req.Msg))
}

func (s *peerService) Grant(ctx context.Context, req *connect.Request[v1.GrantRequest]) (*connect.Response[v1.GrantResponse], error) {
	c, err := callerKind(ctx, peerMember)
	if err != nil {
		return nil, err
	}
	return answer(s.m.grantFor(c, req.Msg))
}

func (s *peerService) Reissue(ctx context.Context, req *connect.Request[v1.ReissueRequest]) (*connect.Response[v1.ReissueResponse], error) {
	c, err := callerKind(ctx, peerMember)
	if err != nil {
		return nil, err
	}
	return answer(s.m.reissue(c, req.Msg))
}

func (s *peerService) TakeIssuer(ctx context.Context, req *connect.Request[v1.TakeIssuerRequest]) (*connect.Response[v1.TakeIssuerResponse], error) {
	c, err := callerKind(ctx, peerMember)
	if err != nil {
		return nil, err
	}
	return answer(s.m.takeIssuer(c, req.Msg))
}

func (s *peerService) Sync(ctx context.Context, req *connect.Request[v1.SyncRequest]) (*connect.Response[v1.SyncResponse], error) {
	c, err := callerKind(ctx, peerMember)
	if err != nil {
		return nil, err
	}
	return answer(s.m.sync(ctx, c, req.Msg))
}

func (s *peerService) Bye(ctx context.Context, req *connect.Request[v1.ByeRequest]) (*connect.Response[v1.ByeResponse], error) {
	c, err := callerKind(ctx, peerMember)
	if err != nil {
		return nil, err
	}
	if err := s.m.bye(c); err != nil {
		return nil, coded(err)
	}
	return connect.NewResponse(&v1.ByeResponse{}), nil
}

func (s *peerService) Stream(ctx context.Context, req *connect.Request[v1.StreamRequest], stream *connect.ServerStream[v1.StreamResponse]) error {
	if _, err := callerKind(ctx, peerMember); err != nil {
		return err
	}
	return links.Stream(ctx, req.Msg.GetBytes(), stream.Send)
}

func (s *peerService) Knock(ctx context.Context, req *connect.Request[v1.KnockRequest]) (*connect.Response[v1.KnockResponse], error) {
	c, err := callerKind(ctx, peerStranger)
	if err != nil {
		return nil, err
	}
	return answer(s.m.knock(ctx, c, req.Msg))
}

func (s *peerService) Offer(ctx context.Context, req *connect.Request[v1.OfferRequest]) (*connect.Response[v1.OfferResponse], error) {
	c, err := callerKind(ctx, peerStranger)
	if err != nil {
		return nil, err
	}
	return answer(s.m.offer(ctx, c, req.Msg))
}

func (s *peerService) Welcome(ctx context.Context, req *connect.Request[v1.WelcomeRequest]) (*connect.Response[v1.WelcomeResponse], error) {
	c, err := callerKind(ctx, peerStranger)
	if err != nil {
		return nil, err
	}
	return answer(s.m.welcome(ctx, c, req.Msg))
}

func (s *peerService) Federate(ctx context.Context, req *connect.Request[v1.FederateRequest]) (*connect.Response[v1.FederateResponse], error) {
	c, err := callerKind(ctx, peerStranger)
	if err != nil {
		return nil, err
	}
	return answer(s.m.federate(ctx, c, req.Msg))
}

func (s *peerService) Exports(ctx context.Context, req *connect.Request[v1.ExportsRequest]) (*connect.Response[v1.ExportsResponse], error) {
	c, err := callerKind(ctx, peerImporter)
	if err != nil {
		return nil, err
	}
	return answer(s.m.exports(c))
}
