package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

// Sends every request to the test server whatever host it names
type toServer struct{ base *url.URL }

func (t toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = t.base.Scheme, t.base.Host
	return http.DefaultTransport.RoundTrip(r)
}

// An id token naming the ChatGPT workspace the Codex endpoint takes in its header
func idToken(workspace string) string {
	claims, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": workspace}})
	return "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

// The providers' sign-in services: Codex's device flow with form exchange and JSON refresh, OpenAI's documented
// redirect flow issuing a client, and OpenRouter's code for a key
func authServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var polls, refreshes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["client_id"] == "" {
				http.Error(w, "no client", http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"device_auth_id":"dev-1","user_code":"ABCD-1234","interval":"1"}`)
		case "/api/accounts/deviceauth/token":
			if polls.Add(1) == 1 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			io.WriteString(w, `{"authorization_code":"code-1","code_challenge":"c","code_verifier":"verifier-1"}`)
		case "/oauth/token":
			if r.Header.Get("Content-Type") == "application/json" {
				var body map[string]string
				json.NewDecoder(r.Body).Decode(&body)
				if body["grant_type"] != "refresh_token" || body["refresh_token"] != "refresh-1" || body["client_id"] == "" {
					http.Error(w, `{"error":"invalid_grant","error_description":"echoes refresh-1"}`, http.StatusBadRequest)
					return
				}
				refreshes.Add(1)
				json.NewEncoder(w).Encode(map[string]any{"access_token": "access-2", "refresh_token": "refresh-2", "token_type": "Bearer", "expires_in": 3600})
				return
			}
			r.ParseForm()
			if r.Form.Get("code") != "code-1" || r.Form.Get("code_verifier") != "verifier-1" || r.Form.Get("client_id") == "" || r.Form.Get("redirect_uri") == "" {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "id_token": idToken("acct-1"), "token_type": "Bearer", "expires_in": 3600})
		case "/api/accounts/oauth/token":
			r.ParseForm()
			if r.Form.Get("client_id") != "oaiapp_issued" || r.Form.Get("resource") != "https://api.openai.com/v1" {
				http.Error(w, `{"error":"invalid_client"}`, http.StatusBadRequest)
				return
			}
			if r.Form.Get("grant_type") == "authorization_code" && (r.Form.Get("code") != "plan-code" || r.Form.Get("code_verifier") == "") {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			access := "plan-access"
			if r.Form.Get("grant_type") == "refresh_token" {
				if r.Form.Get("refresh_token") != "plan-refresh" {
					http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
					return
				}
				access = "plan-access-2"
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "plan-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": "openid offline_access resource.invoke chatgpt.tokens.use.direct"})
		case "/api/v1/auth/keys":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["code"] != "or-code" || body["code_verifier"] == "" || body["code_challenge_method"] != "S256" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			io.WriteString(w, `{"key":"sk-or-issued"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &refreshes
}

func openCredentials(t *testing.T) *Credentials {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	rows := append(sources.Seeds(), &v1.Source{Id: "lab", Kind: v1.SourceKind_SOURCE_KIND_OPENAI_COMPATIBLE, Config: map[string]string{"endpoint": "http://127.0.0.1:1/v1"}})
	m := sources.NewManager(store, nil, slog.New(slog.DiscardHandler), t.TempDir())
	if err := m.Load(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	return NewCredentials(store, m.Registry, slog.New(slog.DiscardHandler), "urn:uuid:00000000-0000-4000-8000-000000000000")
}

func TestCredentialsBelongToTheirOwner(t *testing.T) {
	ctx := context.Background()
	c := openCredentials(t)
	if _, err := c.Put(ctx, "ann", &v1.PutCredentialRequest{SourceId: "huggingface", Secret: "k"}); !errors.Is(err, ErrProviderCredential) {
		t.Fatalf("a catalog of weights takes no credential, got %v", err)
	}
	if _, err := c.Put(ctx, "ann", &v1.PutCredentialRequest{SourceId: "lab"}); !errors.Is(err, ErrProviderCredential) {
		t.Fatalf("a new key needs its secret, got %v", err)
	}
	if _, err := c.Put(ctx, "ann", &v1.PutCredentialRequest{SourceId: "codex", Secret: "k"}); !errors.Is(err, ErrProviderCredential) {
		t.Fatalf("a sign-in provider takes no pasted key, got %v", err)
	}
	first, err := c.Put(ctx, "ann", &v1.PutCredentialRequest{SourceId: "lab", Secret: " sk-one "})
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Put(ctx, "ann", &v1.PutCredentialRequest{SourceId: "lab", Name: "spare", Secret: "sk-two"})
	if err != nil {
		t.Fatal(err)
	}
	if first.GetPosition() != 1 || second.GetPosition() != 2 || !first.GetSecretSet() || first.GetName() == "" || first.GetKind() != v1.CredentialKind_CREDENTIAL_KIND_KEY {
		t.Fatalf("credentials %v %v", first, second)
	}
	signer, h, err := c.Headers(ctx, "ann", "lab", "", nil)
	if err != nil || signer.GetId() != first.GetId() || h["Authorization"] != "Bearer sk-one" {
		t.Fatalf("the first by position signs, got %v %v %v", h, signer, err)
	}
	if _, _, err := c.Headers(ctx, "bob", "lab", "", nil); !errors.Is(err, sources.ErrNoCredential) {
		t.Fatalf("another owner keeps none, got %v", err)
	}
	if signer, h, err := c.Headers(ctx, "ann", "huggingface", "", nil); signer != nil || h != nil || err != nil {
		t.Fatalf("a catalog of weights is no provider, got %v %v %v", h, signer, err)
	}
	if _, err := c.Put(ctx, "bob", &v1.PutCredentialRequest{Id: first.GetId(), Secret: "sk-stolen"}); !errors.Is(err, ErrUnknownCredential) {
		t.Fatalf("another owner's credential is unknown, got %v", err)
	}
	// An empty key keeps the stored one, and a position reorders
	if _, err := c.Put(ctx, "ann", &v1.PutCredentialRequest{Id: second.GetId(), Position: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Put(ctx, "ann", &v1.PutCredentialRequest{Id: first.GetId(), Position: 3}); err != nil {
		t.Fatal(err)
	}
	if _, h, _ := c.Headers(ctx, "ann", "lab", "", nil); h["Authorization"] != "Bearer sk-two" {
		t.Fatalf("the reordered first signs, got %v", h)
	}
	list, err := c.List(ctx, "ann")
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v %v", list, err)
	}
	if err := c.Delete(ctx, "bob", first.GetId()); !errors.Is(err, ErrUnknownCredential) {
		t.Fatalf("another owner cannot remove it, got %v", err)
	}
	if err := c.Delete(ctx, "ann", first.GetId()); err != nil {
		t.Fatal(err)
	}
	if CredentialOwner(Identity{}) != ProviderDaemon || CredentialOwner(Identity{Provider: ProviderDaemon}) != ProviderDaemon || CredentialOwner(Identity{Provider: "local", Subject: "u1"}) != "u1" {
		t.Fatal("owners")
	}
}

func TestSignInKeepsAndRefreshesTokens(t *testing.T) {
	ctx := context.Background()
	srv, refreshes := authServer(t)
	base, _ := url.Parse(srv.URL)
	c := openCredentials(t)
	c.client = &http.Client{Transport: toServer{base}}
	if _, err := c.StartSignIn(ctx, "ann", "deepseek", ""); !errors.Is(err, ErrProviderCredential) {
		t.Fatalf("a key provider takes no sign-in, got %v", err)
	}
	in, err := c.StartSignIn(ctx, "ann", "codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if in.GetCode() != "ABCD-1234" || in.GetUrl() != "https://auth.openai.com/codex/device" || in.GetId() == "" {
		t.Fatalf("sign-in %v", in)
	}
	if _, err := c.FinishSignIn(ctx, "bob", in.GetId(), ""); !errors.Is(err, ErrUnknownCredential) {
		t.Fatalf("another owner's sign-in is unknown, got %v", err)
	}
	cred, err := c.FinishSignIn(ctx, "ann", in.GetId(), "")
	if err != nil {
		t.Fatal(err)
	}
	if cred.GetKind() != v1.CredentialKind_CREDENTIAL_KIND_SIGN_IN || cred.GetExpiresAt() == nil || cred.GetName() != "Codex" {
		t.Fatalf("credential %v", cred)
	}
	_, h, err := c.Headers(ctx, "ann", "codex", "", nil)
	if err != nil || h["Authorization"] != "Bearer access-1" || h["chatgpt-account-id"] != "acct-1" || h["originator"] == "" {
		t.Fatalf("headers %v %v", h, err)
	}
	// Close to its expiry the access is refreshed and kept, the id token carried over
	c.now = func() time.Time { return time.Now().Add(58 * time.Minute) }
	_, h, err = c.Headers(ctx, "ann", "codex", "", nil)
	if err != nil || h["Authorization"] != "Bearer access-2" || h["chatgpt-account-id"] != "acct-1" || refreshes.Load() != 1 {
		t.Fatalf("refreshed headers %v %v, %d refreshes", h, err, refreshes.Load())
	}
	if _, h, _ = c.Headers(ctx, "ann", "codex", "", nil); h["Authorization"] != "Bearer access-2" || refreshes.Load() != 1 {
		t.Fatalf("a refreshed sign-in is not refreshed again, %v %d", h, refreshes.Load())
	}
	// A refused refresh past the expiry names no token the provider echoed
	c.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	if _, _, err := c.Headers(ctx, "ann", "codex", "", nil); err == nil || strings.Contains(err.Error(), "refresh-") {
		t.Fatalf("refused refresh %v", err)
	}
}

func TestSignInByRedirect(t *testing.T) {
	ctx := context.Background()
	srv, _ := authServer(t)
	base, _ := url.Parse(srv.URL)
	c := openCredentials(t)
	c.client = &http.Client{Transport: toServer{base}}
	in, err := c.StartSignIn(ctx, "ann", "openai", "plan")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(in.GetUrl())
	q := u.Query()
	if u.Host != "auth.openai.com" || q.Get("client_id") != "dynamic_agent_client" || q.Get("code_challenge_method") != "S256" || q.Get("ext_agent_host_id") != c.host || q.Get("nonce") == "" || q.Get("agent_name_hint") != "nebu" || in.GetCode() != "" {
		t.Fatalf("authorize address %s", in.GetUrl())
	}
	if _, err := c.FinishSignIn(ctx, "ann", in.GetId(), "http://127.0.0.1:1455/auth/callback?code=plan-code&state=other&client_id=oaiapp_issued"); !errors.Is(err, ErrProviderCredential) {
		t.Fatalf("another sign-in's state is refused, got %v", err)
	}
	cred, err := c.FinishSignIn(ctx, "ann", in.GetId(), "http://127.0.0.1:1455/auth/callback?code=plan-code&state="+q.Get("state")+"&client_id=oaiapp_issued")
	if err != nil {
		t.Fatal(err)
	}
	if cred.GetKind() != v1.CredentialKind_CREDENTIAL_KIND_SIGN_IN || cred.GetName() != "plan" {
		t.Fatalf("credential %v", cred)
	}
	if _, h, err := c.Headers(ctx, "ann", "openai", "", nil); err != nil || h["Authorization"] != "Bearer plan-access" {
		t.Fatalf("headers %v %v", h, err)
	}
	// The issued client signs its refresh, with the resource the provider asks for
	c.now = func() time.Time { return time.Now().Add(58 * time.Minute) }
	if _, h, err := c.Headers(ctx, "ann", "openai", "", nil); err != nil || h["Authorization"] != "Bearer plan-access-2" {
		t.Fatalf("refreshed headers %v %v", h, err)
	}

	c.now = time.Now
	in, err = c.StartSignIn(ctx, "ann", "openrouter", "")
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := url.Parse(in.GetUrl()); u.Host != "openrouter.ai" || u.Query().Get("code_challenge") == "" {
		t.Fatalf("authorize address %s", in.GetUrl())
	}
	cred, err = c.FinishSignIn(ctx, "ann", in.GetId(), "or-code")
	if err != nil {
		t.Fatal(err)
	}
	if cred.GetKind() != v1.CredentialKind_CREDENTIAL_KIND_KEY || !cred.GetSecretSet() {
		t.Fatalf("a sign-in ending in a key keeps a key, got %v", cred)
	}
	if _, h, _ := c.Headers(ctx, "ann", "openrouter", "", nil); h["Authorization"] != "Bearer sk-or-issued" {
		t.Fatalf("headers %v", h)
	}
}
