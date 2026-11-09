package rpc

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/grpcreflect"
	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A daemon's credentials over a fresh store, with an inference token and a read token of an admin account
func surfaceDeps(t *testing.T) (Deps, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions, err := auth.OpenSessions(filepath.Join(dir, "session.key"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tokens := auth.NewTokens(store, log)
	users := auth.NewUsers(store, log, tokens)
	sessions.Validate(users.Valid)
	ann, err := users.Setup(context.Background(), "ann", "ann-password")
	if err != nil {
		t.Fatal(err)
	}
	me := auth.Identity{Subject: ann.GetId(), Provider: auth.ProviderLocal, Name: "ann", Role: v1.Role_ROLE_ADMIN, Scopes: auth.RoleScopes(v1.Role_ROLE_ADMIN)}
	secrets := map[string]string{}
	for _, s := range []v1.Scope{v1.Scope_SCOPE_INFERENCE, v1.Scope_SCOPE_READ} {
		tok, err := tokens.Create(context.Background(), me, auth.ScopeName(s), []v1.Scope{s})
		if err != nil {
			t.Fatal(err)
		}
		secrets[auth.ScopeName(s)] = tok.GetSecret()
	}
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("web ui")) })
	return Deps{Guard: auth.NewGuard("daemon-token", sessions, tokens), Sessions: sessions, Users: users, Tokens: tokens, Log: log, Web: web, CacheDir: dir}, secrets
}

// The handler the deps build, failing the test when it does not build
func handlerOf(t *testing.T, deps Deps) http.Handler {
	t.Helper()
	h, err := NewHandler(deps)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func status(t *testing.T, srv *httptest.Server, method, path, bearer string) int {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// The mesh surface mounts no setup, no web UI, no reflection, and no service beyond what members call
func TestMeshSurfaceMountsNothingExtra(t *testing.T) {
	deps, _ := surfaceDeps(t)
	deps.MeshSurface = true
	srv := httptest.NewServer(handlerOf(t, deps))
	t.Cleanup(srv.Close)
	for _, path := range []string{
		"/auth/session", "/auth/local/setup", "/auth/local/login", "/", "/index.html", "/mcp", "/files",
		"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", "/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo",
		"/nebu.v1.SettingsService/GetSettings", "/nebu.v1.AuthService/ListUsers", "/nebu.v1.HostService/GetProfile",
		"/nebu.v1.InstanceService/Run", "/nebu.v1.EventService/WatchEvents", "/nebu.v1.ChatService/ListConversations",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if code := status(t, srv, method, path, "daemon-token"); code != http.StatusNotFound {
				t.Errorf("%s %s on the mesh surface answered %d", method, path, code)
			}
		}
	}
	// What members call is mounted and guarded
	if code := status(t, srv, http.MethodPost, "/nebu.v1.MeshService/GetMesh", ""); code != http.StatusUnauthorized {
		t.Errorf("mesh service without a credential %d", code)
	}
	if code := status(t, srv, http.MethodPost, "/nebu.v1.ToolService/ListToolServers", ""); code != http.StatusUnauthorized {
		t.Errorf("tool service without a credential %d", code)
	}
	if code := status(t, srv, http.MethodGet, "/blobs/sha256:"+strings.Repeat("0", 64), ""); code != http.StatusUnauthorized {
		t.Errorf("blobs without a member session %d", code)
	}
}

// The API surface serves setup and the UI, and puts reflection behind the read scope
func TestAPISurfaceGuardsReflection(t *testing.T) {
	deps, secrets := surfaceDeps(t)
	srv := httptest.NewUnstartedServer(handlerOf(t, deps))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	if code := status(t, srv, http.MethodGet, "/auth/session", ""); code != http.StatusOK {
		t.Fatalf("session status %d", code)
	}
	if code := status(t, srv, http.MethodPost, "/auth/local/setup", ""); code != http.StatusConflict {
		t.Fatalf("setup after the first account %d", code)
	}
	if code := status(t, srv, http.MethodGet, "/", ""); code != http.StatusOK {
		t.Fatalf("web ui %d", code)
	}
	list := func(bearer string) error {
		h := http.Header{}
		if bearer != "" {
			h.Set("Authorization", "Bearer "+bearer)
		}
		stream := grpcreflect.NewClient(srv.Client(), srv.URL).NewStream(context.Background(), grpcreflect.WithRequestHeaders(h))
		defer stream.Close()
		names, err := stream.ListServices()
		if err == nil && len(names) == 0 {
			t.Fatal("reflection listed no services")
		}
		return err
	}
	if err := list(""); err == nil {
		t.Fatal("reflection without a credential answered")
	}
	if err := list(secrets["inference"]); err == nil {
		t.Fatal("reflection with an inference token answered")
	}
	if err := list(secrets["read"]); err != nil {
		t.Fatalf("reflection with a read token %v", err)
	}
	if err := list("daemon-token"); err != nil {
		t.Fatalf("reflection with the daemon token %v", err)
	}
}
