package sso

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// What /auth/session answers
type view struct {
	SSO  *struct{ Name string }
	User *struct{ Subject, Email, Name, Provider string }
}

// A provider under test control: it signs with RSA, checks the exchange, and serves userinfo
type fakeIdP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	nonce  string
	claims map[string]any
	// Userinfo claims, nil for no userinfo endpoint
	userinfo map[string]any
	// Overrides for token contents
	exp       time.Time
	aud       any
	tokenAuth []string
	pkce      []string
	// What the token endpoint saw
	seenAuth     string
	seenVerifier string
	seenRedirect string
	seenBearer   string
}

func newIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeIdP{key: key, claims: map[string]any{}, exp: time.Now().Add(time.Hour)}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{
			"issuer":                 p.srv.URL + "/",
			"authorization_endpoint": p.srv.URL + "/authorize?tenant=x",
			"token_endpoint":         p.srv.URL + "/token",
			"jwks_uri":               p.srv.URL + "/jwks",
		}
		if p.userinfo != nil {
			doc["userinfo_endpoint"] = p.srv.URL + "/userinfo"
		}
		if p.tokenAuth != nil {
			doc["token_endpoint_auth_methods_supported"] = p.tokenAuth
		}
		if p.pkce != nil {
			doc["code_challenge_methods_supported"] = p.pkce
		}
		json.NewEncoder(w).Encode(doc)
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := p.key.PublicKey
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		p.mu.Lock()
		defer p.mu.Unlock()
		if user, pass, ok := r.BasicAuth(); ok {
			p.seenAuth = "basic " + user + ":" + pass
		} else if r.PostForm.Get("client_secret") != "" {
			p.seenAuth = "post " + r.PostForm.Get("client_id") + ":" + r.PostForm.Get("client_secret")
		} else {
			p.seenAuth = "none " + r.PostForm.Get("client_id")
		}
		p.seenVerifier, p.seenRedirect = r.PostForm.Get("code_verifier"), r.PostForm.Get("redirect_uri")
		if r.PostForm.Get("code") != "good-code" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "unknown code"})
			return
		}
		claims := map[string]any{"iss": p.srv.URL + "/", "sub": "u1", "aud": "nebu", "exp": p.exp.Unix(), "iat": time.Now().Unix(), "nonce": p.nonce}
		if p.aud != nil {
			claims["aud"] = p.aud
		}
		for k, v := range p.claims {
			claims[k] = v
		}
		json.NewEncoder(w).Encode(map[string]string{"id_token": signRS256(t, p.key, "k1", claims), "access_token": "at-1", "token_type": "Bearer"})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.seenBearer = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(p.userinfo)
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func signRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
	body, _ := json.Marshal(claims)
	signed := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(body)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// Builds the service and a server mounting it, with a cookie jar client that stops at redirects
func harness(t *testing.T, idp *fakeIdP, cfg *v1.Oidc) (*Service, *httptest.Server, *http.Client) {
	t.Helper()
	cfg.Issuer = idp.srv.URL
	if cfg.ClientId == "" {
		cfg.ClientId = "nebu"
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	if cfg.Name == "" {
		cfg.Name = "Test IdP"
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions, err := auth.OpenSessions(filepath.Join(t.TempDir(), "session.key"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(cfg, sessions, log)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(auth.Handler(auth.Options{Sessions: sessions, SSO: cfg.Name, SSOHandler: Handler(svc), Log: log}))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return svc, srv, client
}

// Runs the browser side of the code flow and returns the callback response
func signIn(t *testing.T, idp *fakeIdP, srv *httptest.Server, client *http.Client, next string) (*http.Response, url.Values) {
	t.Helper()
	resp, err := client.Get(srv.URL + loginPath + "?next=" + url.QueryEscape(next))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login status %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := loc.Query()
	idp.mu.Lock()
	idp.nonce = q.Get("nonce")
	idp.mu.Unlock()
	cb, err := client.Get(q.Get("redirect_uri") + "?code=good-code&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	return cb, q
}

func sessionStatus(t *testing.T, srv *httptest.Server, client *http.Client) view {
	t.Helper()
	resp, err := client.Get(srv.URL + "/auth/session")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st view
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestCodeFlowSignsInAndOut(t *testing.T) {
	idp := newIdP(t)
	idp.claims["email"] = "ann@example.com"
	idp.claims["name"] = "Ann"
	svc, srv, client := harness(t, idp, &v1.Oidc{ClientSecret: "s3cret", AllowedDomains: []string{"example.com"}, Scopes: []string{"profile", "email"}})
	if st := sessionStatus(t, srv, client); st.SSO == nil || st.SSO.Name != "Test IdP" || st.User != nil {
		t.Fatalf("status before sign-in %+v", st)
	}
	cb, q := signIn(t, idp, srv, client, "/chat?x=1")
	body, _ := io.ReadAll(cb.Body)
	cb.Body.Close()
	if cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != "/chat?x=1" {
		t.Fatalf("callback %d %s %s", cb.StatusCode, cb.Header.Get("Location"), body)
	}
	if !strings.HasPrefix(q.Get("scope"), "openid ") || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != srv.URL+callbackPath {
		t.Fatalf("authorize params %v", q)
	}
	idp.mu.Lock()
	seenAuth, seenVerifier, seenRedirect := idp.seenAuth, idp.seenVerifier, idp.seenRedirect
	idp.mu.Unlock()
	if seenAuth != "basic nebu:s3cret" || seenRedirect != srv.URL+callbackPath {
		t.Fatalf("token request auth %q redirect %q", seenAuth, seenRedirect)
	}
	if sum := sha256.Sum256([]byte(seenVerifier)); base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") {
		t.Fatal("verifier does not match the challenge")
	}
	st := sessionStatus(t, srv, client)
	if st.User == nil || st.User.Email != "ann@example.com" || st.User.Name != "Ann" || st.User.Subject != "u1" || st.User.Provider != auth.ProviderOIDC {
		t.Fatalf("status after sign-in %+v", st)
	}
	// The session cookie authenticates plain header checks the API and gateway make
	u, _ := url.Parse(srv.URL)
	h := http.Header{}
	for _, c := range client.Jar.Cookies(u) {
		h.Add("Cookie", c.String())
	}
	if !svc.sessions.Authenticated(h) {
		t.Fatal("cookie should authenticate")
	}
	if svc.sessions.Authenticated(http.Header{"Cookie": {"nebu_session=garbage"}}) {
		t.Fatal("garbage cookie should not authenticate")
	}
	for _, c := range client.Jar.Cookies(u) {
		if c.Name == loginCookie {
			t.Fatal("login cookie should be cleared after the callback")
		}
	}
	resp, err := client.Post(srv.URL+"/auth/logout", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout %d", resp.StatusCode)
	}
	if st := sessionStatus(t, srv, client); st.User != nil {
		t.Fatal("still signed in after logout")
	}
}

func TestCallbackRejectsTamperedFlows(t *testing.T) {
	idp := newIdP(t)
	idp.claims["email"] = "ann@example.com"
	_, srv, client := harness(t, idp, &v1.Oidc{})
	get := func(u string) (int, string) {
		resp, err := client.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(body)
	}
	// No login cookie
	if code, body := get(srv.URL + callbackPath + "?code=good-code&state=x"); code != http.StatusBadRequest || !strings.Contains(body, "No sign-in is in progress") {
		t.Fatalf("no cookie %d %s", code, body)
	}
	start := func() url.Values {
		resp, err := client.Get(srv.URL + loginPath)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		loc, _ := url.Parse(resp.Header.Get("Location"))
		idp.mu.Lock()
		idp.nonce = loc.Query().Get("nonce")
		idp.mu.Unlock()
		return loc.Query()
	}
	q := start()
	if code, body := get(q.Get("redirect_uri") + "?code=good-code&state=other"); code != http.StatusBadRequest || !strings.Contains(body, "different sign-in") {
		t.Fatalf("state mismatch %d %s", code, body)
	}
	q = start()
	if code, body := get(q.Get("redirect_uri") + "?error=access_denied&error_description=nope&state=" + q.Get("state")); code != http.StatusForbidden || !strings.Contains(body, "access_denied nope") {
		t.Fatalf("provider error %d %s", code, body)
	}
	q = start()
	if code, body := get(q.Get("redirect_uri") + "?code=bad-code&state=" + q.Get("state")); code != http.StatusBadGateway || !strings.Contains(body, "invalid_grant") {
		t.Fatalf("bad code %d %s", code, body)
	}
	q = start()
	idp.mu.Lock()
	idp.nonce = "stale"
	idp.mu.Unlock()
	if code, body := get(q.Get("redirect_uri") + "?code=good-code&state=" + q.Get("state")); code != http.StatusUnauthorized || !strings.Contains(body, "nonce") {
		t.Fatalf("nonce %d %s", code, body)
	}
	q = start()
	idp.mu.Lock()
	idp.aud = []string{"someone-else"}
	idp.mu.Unlock()
	if code, body := get(q.Get("redirect_uri") + "?code=good-code&state=" + q.Get("state")); code != http.StatusUnauthorized || !strings.Contains(body, "audience") {
		t.Fatalf("aud %d %s", code, body)
	}
	q = start()
	idp.mu.Lock()
	idp.aud = []string{"nebu", "other"}
	idp.exp = time.Now().Add(-time.Hour)
	idp.mu.Unlock()
	if code, body := get(q.Get("redirect_uri") + "?code=good-code&state=" + q.Get("state")); code != http.StatusUnauthorized || !strings.Contains(body, "expired") {
		t.Fatalf("exp %d %s", code, body)
	}
	// A second callback with the same cookie is refused because the cookie was cleared
	q = start()
	idp.mu.Lock()
	idp.exp = time.Now().Add(time.Hour)
	idp.mu.Unlock()
	if code, _ := get(q.Get("redirect_uri") + "?code=good-code&state=" + q.Get("state")); code != http.StatusSeeOther {
		t.Fatalf("multi audience with matching client %d", code)
	}
	if code, body := get(q.Get("redirect_uri") + "?code=good-code&state=" + q.Get("state")); code != http.StatusBadRequest || !strings.Contains(body, "No sign-in is in progress") {
		t.Fatalf("replayed callback %d %s", code, body)
	}
	// A next outside the app lands on the root
	cb, _ := signIn(t, idp, srv, client, "//evil.example")
	cb.Body.Close()
	if cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != "/" {
		t.Fatalf("open redirect %d %s", cb.StatusCode, cb.Header.Get("Location"))
	}
}

func TestAllowListsAndUserinfo(t *testing.T) {
	idp := newIdP(t)
	idp.claims["email"] = "bob@other.org"
	_, srv, client := harness(t, idp, &v1.Oidc{AllowedDomains: []string{"@example.com"}, AllowedEmails: []string{"Carol@Partner.io"}})
	cb, _ := signIn(t, idp, srv, client, "/")
	body, _ := io.ReadAll(cb.Body)
	cb.Body.Close()
	if cb.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "allowed_emails or allowed_domains") {
		t.Fatalf("other domain %d %s", cb.StatusCode, body)
	}
	idp.claims["email"] = "carol@partner.io"
	cb, _ = signIn(t, idp, srv, client, "/")
	cb.Body.Close()
	if cb.StatusCode != http.StatusSeeOther {
		t.Fatalf("listed email should sign in, got %d", cb.StatusCode)
	}
	idp.claims["email"] = "dan@example.com"
	idp.claims["email_verified"] = false
	cb, _ = signIn(t, idp, srv, client, "/")
	body, _ = io.ReadAll(cb.Body)
	cb.Body.Close()
	if cb.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "unverified") {
		t.Fatalf("unverified %d %s", cb.StatusCode, body)
	}

	// Groups missing from the ID token come from userinfo, nested claims resolve by dotted path
	idp2 := newIdP(t)
	idp2.claims["email"] = "eve@example.com"
	idp2.userinfo = map[string]any{"sub": "u1", "realm_access": map[string]any{"roles": []string{"viewer", "nebu-admin"}}}
	_, srv2, client2 := harness(t, idp2, &v1.Oidc{AllowedGroups: []string{"nebu-admin"}, GroupsClaim: "realm_access.roles"})
	cb, _ = signIn(t, idp2, srv2, client2, "/")
	body, _ = io.ReadAll(cb.Body)
	cb.Body.Close()
	if cb.StatusCode != http.StatusSeeOther {
		t.Fatalf("group from userinfo %d %s", cb.StatusCode, body)
	}
	idp2.mu.Lock()
	bearer := idp2.seenBearer
	idp2.mu.Unlock()
	if bearer != "Bearer at-1" {
		t.Fatalf("userinfo bearer %q", bearer)
	}
	idp2.userinfo = map[string]any{"sub": "u1", "realm_access": map[string]any{"roles": []string{"viewer"}}}
	cb, _ = signIn(t, idp2, srv2, client2, "/")
	body, _ = io.ReadAll(cb.Body)
	cb.Body.Close()
	if cb.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "allowed_groups") {
		t.Fatalf("wrong group %d %s", cb.StatusCode, body)
	}
	idp2.userinfo = map[string]any{"sub": "someone-else", "realm_access": map[string]any{"roles": []string{"nebu-admin"}}}
	cb, _ = signIn(t, idp2, srv2, client2, "/")
	body, _ = io.ReadAll(cb.Body)
	cb.Body.Close()
	if cb.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "different subject") {
		t.Fatalf("userinfo subject %d %s", cb.StatusCode, body)
	}
}

func TestProviderNegotiation(t *testing.T) {
	// Public client without a secret, provider that only lists plain PKCE, and client_secret_post only
	idp := newIdP(t)
	idp.claims["email"] = "ann@example.com"
	idp.pkce = []string{"plain"}
	_, srv, client := harness(t, idp, &v1.Oidc{})
	cb, q := signIn(t, idp, srv, client, "/")
	cb.Body.Close()
	if cb.StatusCode != http.StatusSeeOther || q.Get("code_challenge") != "" {
		t.Fatalf("public client without S256: %d challenge %q", cb.StatusCode, q.Get("code_challenge"))
	}
	idp.mu.Lock()
	seen, verifier := idp.seenAuth, idp.seenVerifier
	idp.mu.Unlock()
	if seen != "none nebu" || verifier != "" {
		t.Fatalf("public client auth %q verifier %q", seen, verifier)
	}

	idp2 := newIdP(t)
	idp2.claims["email"] = "ann@example.com"
	idp2.tokenAuth = []string{"client_secret_post"}
	_, srv2, client2 := harness(t, idp2, &v1.Oidc{ClientSecret: "p@ss word"})
	cb, _ = signIn(t, idp2, srv2, client2, "/")
	cb.Body.Close()
	idp2.mu.Lock()
	seen = idp2.seenAuth
	idp2.mu.Unlock()
	if cb.StatusCode != http.StatusSeeOther || seen != "post nebu:p@ss word" {
		t.Fatalf("post auth %d %q", cb.StatusCode, seen)
	}

	idp3 := newIdP(t)
	idp3.tokenAuth = []string{"private_key_jwt"}
	_, srv3, client3 := harness(t, idp3, &v1.Oidc{ClientSecret: "x"})
	cb, _ = signIn(t, idp3, srv3, client3, "/")
	body, _ := io.ReadAll(cb.Body)
	cb.Body.Close()
	if cb.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "private_key_jwt") {
		t.Fatalf("unsupported client auth %d %s", cb.StatusCode, body)
	}

	// Public URL and forwarded headers shape the callback
	idp4 := newIdP(t)
	_, srv4, client4 := harness(t, idp4, &v1.Oidc{PublicUrl: "https://nebu.example.com/"})
	resp, err := client4.Get(srv4.URL + loginPath)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Query().Get("redirect_uri") != "https://nebu.example.com"+callbackPath || loc.Path != "/authorize" || loc.Query().Get("tenant") != "x" {
		t.Fatalf("public url redirect %s", loc)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+loginPath, nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "proxy.example.com")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ = url.Parse(resp.Header.Get("Location"))
	if loc.Query().Get("redirect_uri") != "https://proxy.example.com"+callbackPath {
		t.Fatalf("forwarded redirect %s", loc.Query().Get("redirect_uri"))
	}
	if !strings.Contains(resp.Header.Get("Set-Cookie"), "Secure") {
		t.Fatalf("forwarded https should set a secure cookie: %s", resp.Header.Get("Set-Cookie"))
	}
}

func TestDiscoveryMismatchAndBadConfig(t *testing.T) {
	idp := newIdP(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions, _ := auth.OpenSessions(filepath.Join(t.TempDir(), "k"), time.Hour)
	svc, err := New(&v1.Oidc{Issuer: idp.srv.URL + "/other", ClientId: "nebu", GroupsClaim: "groups"}, sessions, log)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(svc))
	defer srv.Close()
	resp, err := http.Get(srv.URL + loginPath)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "could not be reached") {
		t.Fatalf("bad issuer path %d %s", resp.StatusCode, body)
	}
	for _, bad := range []*v1.Oidc{
		{Issuer: "ftp://x", ClientId: "c", GroupsClaim: "g"},
		{Issuer: idp.srv.URL, GroupsClaim: "g"},
		{Issuer: idp.srv.URL, ClientId: "c"},
		{Issuer: idp.srv.URL, ClientId: "c", GroupsClaim: "g", PublicUrl: "nebu.example.com"},
	} {
		if _, err := New(bad, sessions, log); err == nil {
			t.Fatalf("config %v should fail", bad)
		}
	}
	if _, err := New(&v1.Oidc{Issuer: idp.srv.URL, ClientId: "c", GroupsClaim: "g"}, nil, log); err == nil {
		t.Fatal("nil sessions should fail")
	}
}

func TestSignatureAlgorithms(t *testing.T) {
	claims := map[string]any{"sub": "u1"}
	body, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(body)
	mint := func(alg, kid string, sign func([]byte) []byte) string {
		hdr, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid})
		signed := base64.RawURLEncoding.EncodeToString(hdr) + "." + payload
		return signed + "." + base64.RawURLEncoding.EncodeToString(sign([]byte(signed)))
	}
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	es := mint("ES256", "ec", func(b []byte) []byte {
		sum := sha256.Sum256(b)
		r, s, _ := ecdsa.Sign(rand.Reader, ecKey, sum[:])
		out := make([]byte, 64)
		r.FillBytes(out[:32])
		s.FillBytes(out[32:])
		return out
	})
	edPub, edPriv, _ := ed25519.GenerateKey(rand.Reader)
	ed := mint("EdDSA", "ed", func(b []byte) []byte { return ed25519.Sign(edPriv, b) })
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ps := mint("PS256", "", func(b []byte) []byte {
		sum := sha256.Sum256(b)
		sig, _ := rsa.SignPSS(rand.Reader, rsaKey, crypto.SHA256, sum[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		return sig
	})
	hs := mint("HS256", "", func(b []byte) []byte {
		return hmacSum(b, "secret")
	})
	keys := []jwk{
		{Kty: "EC", Kid: "ec", Crv: "P-256", X: base64.RawURLEncoding.EncodeToString(ecKey.X.Bytes()), Y: base64.RawURLEncoding.EncodeToString(ecKey.Y.Bytes())},
		{Kty: "OKP", Kid: "ed", Crv: "Ed25519", X: base64.RawURLEncoding.EncodeToString(edPub)},
		{Kty: "RSA", Kid: "rsa", N: base64.RawURLEncoding.EncodeToString(rsaKey.N.Bytes()), E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(rsaKey.E)).Bytes())},
	}
	for name, raw := range map[string]string{"ES256": es, "EdDSA": ed, "PS256 without kid": ps} {
		tok, err := parseToken(raw)
		if err != nil {
			t.Fatal(name, err)
		}
		if err := tok.verify(keys, ""); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		tok.sig[0] ^= 0xff
		if err := tok.verify(keys, ""); err == nil {
			t.Fatalf("%s: corrupted signature verified", name)
		}
	}
	tok, _ := parseToken(hs)
	if err := tok.verify(nil, "secret"); err != nil {
		t.Fatal("HS256", err)
	}
	if err := tok.verify(nil, "other"); err == nil {
		t.Fatal("HS256 with the wrong secret verified")
	}
	if err := tok.verify(nil, ""); err == nil {
		t.Fatal("HS256 without a secret verified")
	}
	unknown, _ := parseToken(mint("ES256", "missing", func([]byte) []byte { return make([]byte, 64) }))
	if err := unknown.verify(keys, ""); err != errUnknownKey {
		t.Fatalf("unknown kid: %v", err)
	}
	none, _ := parseToken(mint("none", "", func([]byte) []byte { return nil }))
	if err := none.verify(keys, ""); err == nil {
		t.Fatal("alg none verified")
	}
	if _, err := parseToken("a.b"); err == nil {
		t.Fatal("two part token parsed")
	}
}

func TestClaimHelpers(t *testing.T) {
	claims := map[string]any{
		"a.b":          "flat",
		"realm_access": map[string]any{"roles": []any{"x", "y"}},
		"roles":        map[string]any{"admin": map[string]any{}},
		"exp":          json.Number("1700000000"),
		"verified":     "true",
	}
	if v, ok := claimLookup(claims, "a.b"); !ok || v != "flat" {
		t.Fatal("flat key with a dot should win")
	}
	if v, ok := claimLookup(claims, "realm_access.roles"); !ok || len(claimStrings(v)) != 2 {
		t.Fatal("dotted path")
	}
	if v, _ := claimLookup(claims, "roles"); len(claimStrings(v)) != 1 || claimStrings(v)[0] != "admin" {
		t.Fatal("object keys as groups")
	}
	if _, ok := claimLookup(claims, "realm_access.nope.x"); ok {
		t.Fatal("missing path")
	}
	if ts, ok := claimTime(claims, "exp"); !ok || ts.Unix() != 1700000000 {
		t.Fatal("exp as json number")
	}
	if v, ok := claimBool(claims, "verified"); !ok || !v {
		t.Fatal("string bool")
	}
	if claimStrings("") != nil || len(claimStrings("one")) != 1 {
		t.Fatal("string list")
	}
	if safeNext("/ok?x=1") != "/ok?x=1" || safeNext("http://evil") != "/" || safeNext("//evil") != "/" || safeNext("") != "/" {
		t.Fatal("safeNext")
	}
}

func hmacSum(b []byte, secret string) []byte {
	tok := &token{alg: "HS256", signed: b} // brute re-use?
	mac := newHMAC(secret)
	mac.Write(b)
	_ = tok
	_ = tok.alg
	_ = tok.signed
	return mac.Sum(nil)
}
