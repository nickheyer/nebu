package rpc

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/formations"
	"github.com/nickheyer/nebu/internal/mesh/links"
	"github.com/nickheyer/nebu/pkg/store"
)

const (
	// Members read each other's blobs here, by digest, under the session credential
	blobsPath = "/blobs/"
	// Members fetch a relay's saved slot file here
	slotPath = "/mesh/slot"
)

// Serves peers alone: the handler runs when the request carries a member session
type peerOnly struct {
	guard *auth.Guard
	next  http.Handler
}

func (p *peerOnly) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.guard.PeerOf(r.Header); !ok {
		http.Error(w, "this path is for mesh members and the request carries no member session", http.StatusUnauthorized)
		return
	}
	p.next.ServeHTTP(w, r)
}

// Streams a stored blob to a member with range support, the same shape as /files
type blobs struct {
	store *store.Store
}

func (b *blobs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "blobs are read with GET", http.StatusMethodNotAllowed)
		return
	}
	digest := strings.TrimPrefix(r.URL.Path, blobsPath)
	if !strings.HasPrefix(digest, "sha256:") || strings.ContainsAny(digest, "/\\") || len(digest) != len("sha256:")+64 {
		http.Error(w, "a blob is named by its sha256 digest", http.StatusBadRequest)
		return
	}
	if !b.store.HasBlob(digest) {
		http.Error(w, "this node holds no blob "+digest, http.StatusNotFound)
		return
	}
	f, err := os.Open(b.store.BlobPath(digest))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, digest, info.ModTime(), f)
}

// Serves a relay's saved slot file to the decode seat's node, with its sha256 digest in a
// header, so the node it lands on verifies what it received
type slotFiles struct {
	// Where relay seats save slots: <cache dir>/slots/<formation>/<file>
	dir string
}

func (s *slotFiles) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "slot files are read with GET", http.StatusMethodNotAllowed)
		return
	}
	formation, file := r.URL.Query().Get("formation"), r.URL.Query().Get("file")
	if formation == "" || file == "" || strings.ContainsAny(formation+file, "/\\") || strings.HasPrefix(file, ".") {
		http.Error(w, "formation and file name the slot", http.StatusBadRequest)
		return
	}
	path := filepath.Join(s.dir, "slots", formation, file)
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "no slot file "+file+" for "+formation, http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "no slot file "+file+" for "+formation, http.StatusNotFound)
		return
	}
	digest, err := slotDigest(f)
	if err != nil {
		http.Error(w, "slot file "+file+" could not be read: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set(formations.SlotDigestHeader, digest)
	w.Header().Set("ETag", `"`+strings.TrimPrefix(digest, "sha256:")+`"`)
	http.ServeContent(w, r, file, info.ModTime(), f)
}

// The sha256 digest of a file, the file left at its start
func slotDigest(f *os.File) (string, error) {
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil)), nil
}

// Mounts the paths members use between themselves
func mountMesh(mux *http.ServeMux, guard *auth.Guard, st *store.Store, cacheDir string) {
	mux.Handle(links.SinkPath, &peerOnly{guard: guard, next: http.HandlerFunc(links.Sink)})
	mux.Handle(blobsPath, &peerOnly{guard: guard, next: &blobs{store: st}})
	mux.Handle(slotPath, &peerOnly{guard: guard, next: &slotFiles{dir: cacheDir}})
}
