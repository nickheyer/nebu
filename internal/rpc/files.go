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

	"github.com/nickheyer/nebu/internal/inspect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

// Where a browser fetches one file of a repository through the daemon
const filesPath = "/files"

// Streams one file of a repository through the daemon, so a browser saves it with the daemon's own
// tokens and through whichever transport the source uses
//
// The query names the source, the repo, its revision, and the path; the API token travels in the
// Authorization header or, for a plain link, in the token query field. Ranges are honored so a
// download can resume.
type files struct {
	inspector *inspect.Inspector
	auth      *auth
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
	if !f.auth.ok(header) {
		http.Error(w, errUnauthenticated.Error(), http.StatusUnauthorized)
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
	// The read outlives the handler's context only as far as the response does, so the source's
	// own blob is closed when the response ends
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

// Answers a resolve or open failure with the status the source's own answer carried, else as a bad gateway
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
