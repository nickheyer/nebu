package rpc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/inspect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

const (
	// Repository file download path.
	filesPath = "/files"
	// How long a download link stays good, enough for a browser to resume a large file
	grantTTL     = 10 * time.Minute
	grantBytes   = 32
	grantBodyMax = 16 << 10
)

// One file a download link opens
type grant struct {
	key     string
	expires time.Time
}

// Streams repository files using the source's transport and credentials.
// Query params select source, repository, revision, and path. A GET carries the Authorization
// header, the session cookie, or a link a POST with those credentials made. Range requests support resuming.
type files struct {
	inspector *inspect.Inspector
	auth      *auth.Guard
	log       *slog.Logger
	mu        sync.Mutex
	grants    map[string]grant
}

func (f *files) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		f.issue(w, r)
		return
	case http.MethodGet, http.MethodHead:
	default:
		http.Error(w, "files are read with GET, links made with POST", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	if code := q.Get("grant"); code != "" {
		if !f.redeem(code, grantKey(q)) {
			http.Error(w, "the download link expired or names another file, ask for a new one", http.StatusUnauthorized)
			return
		}
	} else if !f.auth.Authenticated(r.Header) {
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

// The file a link is good for
func grantKey(q url.Values) string {
	return q.Get("source") + "\x00" + q.Get("repo") + "\x00" + q.Get("revision") + "\x00" + q.Get("path")
}

// Makes a download link for one file on behalf of the credentials the request carries
func (f *files) issue(w http.ResponseWriter, r *http.Request) {
	if !f.auth.Authenticated(r.Header) {
		http.Error(w, f.auth.Err().Error(), http.StatusUnauthorized)
		return
	}
	var in struct {
		Source   string `json:"source"`
		Repo     string `json:"repo"`
		Revision string `json:"revision"`
		Path     string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, grantBodyMax)).Decode(&in); err != nil {
		http.Error(w, "a link request is JSON naming source, repo, revision, and path", http.StatusBadRequest)
		return
	}
	if in.Source == "" || in.Repo == "" || in.Path == "" {
		http.Error(w, "source, repo, and path are required", http.StatusBadRequest)
		return
	}
	raw := make([]byte, grantBytes)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	q := url.Values{"source": {in.Source}, "repo": {in.Repo}, "path": {in.Path}, "grant": {code}}
	if in.Revision != "" {
		q.Set("revision", in.Revision)
	}
	now := time.Now()
	f.mu.Lock()
	if f.grants == nil {
		f.grants = map[string]grant{}
	}
	for c, g := range f.grants {
		if now.After(g.expires) {
			delete(f.grants, c)
		}
	}
	f.grants[code] = grant{key: grantKey(q), expires: now.Add(grantTTL)}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]string{"url": filesPath + "?" + q.Encode()})
}

// Whether a link is live and names the file asked for
func (f *files) redeem(code, key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.grants[code]
	return ok && g.key == key && time.Now().Before(g.expires)
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
