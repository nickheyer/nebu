// Package services implements the Connect service handlers.
package services

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/monitor"
	"github.com/nickheyer/nebu/internal/slots"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/build"
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
		errors.Is(err, installs.ErrUnknownInstall), errors.Is(err, instances.ErrUnknownInstance),
		errors.Is(err, installs.ErrUnknownBuild), errors.Is(err, build.ErrUnknownRecipe),
		errors.Is(err, slots.ErrUnknownSlot), errors.Is(err, monitor.ErrUnknownWatch), errors.Is(err, monitor.ErrUnknownFinding):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, runtime.ErrParam), errors.Is(err, build.ErrSelection), errors.Is(err, slots.ErrSlot), errors.Is(err, monitor.ErrWatch):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, transfer.ErrDigestMismatch):
		return connect.NewError(connect.CodeDataLoss, err)
	case errors.Is(err, sources.ErrUnsupported):
		return connect.NewError(connect.CodeUnimplemented, err)
	case sources.IsStatus(err, http.StatusNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case sources.IsStatus(err, http.StatusUnauthorized), sources.IsStatus(err, http.StatusForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	case sources.IsStatus(err, http.StatusTooManyRequests):
		return connect.NewError(connect.CodeResourceExhausted, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
