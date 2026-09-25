package rpc

import (
	"net/http"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/pkg/store"
)

// Mounts the paths members use between themselves on a mux built outside the daemon's handler:
// the link probe sink, the blob channel, and relay slot files, each behind the member session
func MountMesh(mux *http.ServeMux, guard *auth.Guard, st *store.Store, cacheDir string) {
	mountMesh(mux, guard, st, cacheDir)
}
