package rpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/inspect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

// Repository file download path.
const filesPath = "/files"

// Streams repository files using the source's transport and credentials.
// Query params select source, repository, revision, and path. Authentication uses
// the Authorization header, the token query param, or the session cookie. Range requests support resuming.
type files struct {
	inspector *inspect.Inspector
	auth      *auth.Guard
	log       *slog.Logger
}

func (f *files) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "files are read with GET", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	header := r.Header.Clone()
	if tok := q.Get("token"); tok != "" && header.Get("Authorization") == "" {
		header.Set("Authorization", "Bearer "+tok)
	}
	if !f.auth.Authenticated(header) {
		http.Error(w, f.auth.Err().Error(), http.StatusUnauthorized)
		return
	}
	source, repo, revision, file := q.Get("source"), q.Get("repo"), q.Get("revision"), q.Get("path")
	if source == "" || repo == "" || file == "" {
		http.Error(w, "source, repo, and path are required", http.StatusBadRequest)
		return
	}
	src, model, err := f.inspector.Resolve(r.Context(), source, repo, revision)
	if err != nil {
		f.fail(w, err)
		return
	}
	var artifact *v1.Artifact
	for _, a := range model.GetArtifacts() {
		if a.GetPath() == file {
			artifact = a
			break
		}
	}
	if artifact == nil {
		http.Error(w, fmt.Sprintf("%s has no file %s at %s", repo, file, model.GetRevision()), http.StatusNotFound)
		return
	}
	// Close the source blob when the response ends.
	blob, err := src.Open(r.Context(), model, artifact)
	if err != nil {
		f.fail(w, err)
		return
	}
	defer blob.Close()
	name := path.Base(file)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.FormatInt(blob.Size(), 10))
	http.ServeContent(w, r, name, time.Time{}, io.NewSectionReader(blob, 0, blob.Size()))
}

// Preserves source error status codes, defaulting to 502.
func (f *files) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		http.Error(w, err.Error(), http.StatusRequestTimeout)
	case errors.Is(err, sources.ErrUnknownSource), sources.IsStatus(err, http.StatusNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case sources.IsStatus(err, http.StatusUnauthorized), sources.IsStatus(err, http.StatusForbidden):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		f.log.Warn("file download failed", "err", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}
