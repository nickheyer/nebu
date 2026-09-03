// Package services implements the Connect service handlers.
package services

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/runtime"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/transfer"
)

// Maps domain errors onto Connect codes
func wrap(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	case errors.Is(err, sources.ErrUnknownSource), errors.Is(err, runtime.ErrUnknownRuntime), errors.Is(err, formats.ErrUnknownGroup),
		errors.Is(err, tasks.ErrUnknownTask), errors.Is(err, store.ErrNotStored),
		errors.Is(err, installs.ErrUnknownInstall), errors.Is(err, instances.ErrUnknownInstance):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, runtime.ErrParam):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, transfer.ErrDigestMismatch):
		return connect.NewError(connect.CodeDataLoss, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
