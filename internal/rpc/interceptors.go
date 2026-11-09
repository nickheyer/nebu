package rpc

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
)

// Checks every request message against its schema rules, refusing a violating one as InvalidArgument with the violations attached
type validating struct {
	v protovalidate.Validator
}

func newValidating() (*validating, error) {
	v, err := protovalidate.New()
	if err != nil {
		return nil, err
	}
	return &validating{v: v}, nil
}

// The refusal for a message that breaks its rules, nil for one that keeps them
func (i *validating) check(msg any) error {
	m, ok := msg.(proto.Message)
	if !ok {
		return connect.NewError(connect.CodeInternal, errors.New("the request is not a protobuf message"))
	}
	err := i.v.Validate(m)
	if err == nil {
		return nil
	}
	var verr *protovalidate.ValidationError
	if !errors.As(err, &verr) {
		return connect.NewError(connect.CodeInternal, err)
	}
	out := connect.NewError(connect.CodeInvalidArgument, verr)
	if detail, derr := connect.NewErrorDetail(verr.ToProto()); derr == nil {
		out.AddDetail(detail)
	}
	return out
}

func (i *validating) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := i.check(req.Any()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (i *validating) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *validating) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(ctx, &validatedConn{StreamingHandlerConn: conn, check: i.check})
	}
}

// A handler stream whose every received message is checked before the handler reads it
type validatedConn struct {
	connect.StreamingHandlerConn
	check func(any) error
}

func (c *validatedConn) Receive(msg any) error {
	if err := c.StreamingHandlerConn.Receive(msg); err != nil {
		return err
	}
	return c.check(msg)
}

// Logs every unary call and every stream's open and close with duration and outcome, failures as warnings
type logging struct {
	log *slog.Logger
}

// Logs a finished call or stream at debug, or at warn with its code when it failed
func (l logging) finish(what, procedure string, start time.Time, err error) {
	attrs := []any{"procedure", procedure, "ms", time.Since(start).Milliseconds()}
	if err != nil {
		l.log.Warn(what, append(attrs, "code", connect.CodeOf(err).String(), "err", err)...)
		return
	}
	l.log.Debug(what, append(attrs, "code", "ok")...)
}

func (l logging) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		resp, err := next(ctx, req)
		l.finish("rpc", req.Spec().Procedure, start, err)
		return resp, err
	}
}

func (l logging) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (l logging) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := time.Now()
		l.log.Debug("rpc stream opened", "procedure", conn.Spec().Procedure, "peer", conn.Peer().Addr)
		err := next(ctx, conn)
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			err = nil
		}
		l.finish("rpc stream closed", conn.Spec().Procedure, start, err)
		return err
	}
}
