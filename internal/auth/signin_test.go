package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
)

// OpenAI's sign-in service as its documents and its discovery document describe it, with what a test turns: the account
// approving, the key set out of reach, what a revocation is answered with, and refreshes that hang or fail
type planServer struct {
	*httptest.Server
	key   *rsa.PrivateKey
	nonce atomic.Value
	// The account the next approval is by
	subject atomic.Value
	// Tokens issued so far, each grant's numbered
	issued atomic.Int32
	// Whether the key set answers a fault, and how often it was read
	keysDown  atomic.Bool
	keysRead  atomic.Int32
	exchanges atomic.Int32
	// What a revocation is answered with, and the forms asked to be revoked
	revokeStatus atomic.Int32
	mu           sync.Mutex
	revoked      []url.Values
	// A refresh waits on this when set, and answers this status when it is not 200
	hang          atomic.Pointer[chan struct{}]
	refreshStatus atomic.Int32
	refreshes     atomic.Int32
}

func newPlanServer(t *testing.T) *planServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &planServer{key: key}
	p.subject.Store("user-1")
	p.revokeStatus.Store(http.StatusOK)
	p.refreshStatus.Store(http.StatusOK)
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/accounts/authorize":
			q := r.URL.Query()
			p.nonce.Store(q.Get("nonce"))
			http.Redirect(w, r, q.Get("redirect_uri")+"?code=plan-code&state="+url.QueryEscape(q.Get("state"))+"&client_id=oaiapp_issued", http.StatusFound)
		case "/.well-known/jwks.json":
			p.keysRead.Add(1)
			if p.keysDown.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/api/accounts/oauth/token":
			r.ParseForm()
			if r.Form.Get("client_id") != "oaiapp_issued" {
				http.Error(w, `{"error":"invalid_client"}`, http.StatusBadRequest)
				return
			}
			if r.Form.Get("grant_type") == "refresh_token" {
				p.refreshes.Add(1)
				if hang := p.hang.Load(); hang != nil {
					<-*hang
				}
				if status := int(p.refreshStatus.Load()); status != http.StatusOK {
					w.WriteHeader(status)
					return
				}
			} else {
				p.exchanges.Add(1)
			}
			n := p.issued.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("access-%d", n), "refresh_token": fmt.Sprintf("refresh-%d", n), "id_token": p.idToken(), "token_type": "Bearer", "expires_in": 3600, "scope": "openid offline_access resource.invoke chatgpt.tokens.use.direct"})
		case "/api/accounts/oauth/revoke":
			r.ParseForm()
			p.mu.Lock()
			p.revoked = append(p.revoked, r.Form)
			p.mu.Unlock()
			w.WriteHeader(int(p.revokeStatus.Load()))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.Close)
	return p
}

// An ID token as OpenAI signs one, for the issued client, the account approving and the nonce the authorize request carried
func (p *planServer) idToken() string {
	claims, _ := json.Marshal(map[string]any{"iss": "https://auth.openai.com", "aud": []string{"oaiapp_issued"}, "sub": p.subject.Load(), "email": "ann@example.com", "exp": time.Now().Add(time.Hour).Unix(), "nonce": p.nonce.Load()})
	body := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"k1","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(body))
	sig, _ := rsa.SignPKCS1v15(nil, p.key, crypto.SHA256, sum[:])
	return body + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (p *planServer) revocations() []url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]url.Values(nil), p.revoked...)
}

// Credentials signing in at the plan server
func planCredentials(t *testing.T) (*Credentials, *planServer) {
	t.Helper()
	p := newPlanServer(t)
	base, _ := url.Parse(p.URL)
	c := openCredentials(t)
	c.client = &http.Client{Transport: toServer{base}, Timeout: 30 * time.Second}
	return c, p
}

// The address the browser lands on after approving a started sign-in
func approve(t *testing.T, c *Credentials, in *v1.ProviderSignIn) string {
	t.Helper()
	browser := &http.Client{Transport: c.client.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := browser.Get(in.GetUrl())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.Header.Get("Location")
}

// Signs an owner in with OpenAI, or one of its sign-ins again
func signIn(t *testing.T, c *Credentials, owner, again string) (*v1.ProviderCredential, error) {
	t.Helper()
	in, err := c.StartSignIn(context.Background(), owner, "openai", "plan", again)
	if err != nil {
		t.Fatal(err)
	}
	return c.FinishSignIn(context.Background(), owner, in.GetId(), approve(t, c, in))
}

func secretOf(t *testing.T, c *Credentials, owner, id string) signedIn {
	t.Helper()
	row, err := c.db.GetCredential(context.Background(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	var s signedIn
	if err := json.Unmarshal([]byte(row.Secret), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Signing in again goes under the issued client with no token or account in the address, takes the same account alone,
// refuses a landing address naming another client, and queues what another account was issued for revocation
func TestSignInAgainKeepsTheAccountAndItsTokenOutOfTheAddress(t *testing.T) {
	ctx := context.Background()
	c, p := planCredentials(t)
	cred, err := signIn(t, c, "ann", "")
	if err != nil {
		t.Fatal(err)
	}
	first := secretOf(t, c, "ann", cred.GetId())
	if first.ClientID != "oaiapp_issued" || first.IDToken == "" || subjectOf(first.IDToken) != "user-1" {
		t.Fatalf("stored sign-in %+v", first)
	}

	in, err := c.StartSignIn(ctx, "ann", "openai", "", cred.GetId())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(in.GetUrl())
	q := u.Query()
	if q.Get("client_id") != "oaiapp_issued" || q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" || q.Get("ext_agent_host_id") != c.host {
		t.Fatalf("the address to authorize again at %s", in.GetUrl())
	}
	for _, name := range []string{"id_token_hint", "login_hint", "agent_name_hint"} {
		if q.Has(name) {
			t.Fatalf("the address to authorize again at carries %s: %s", name, in.GetUrl())
		}
	}
	if strings.Contains(in.GetUrl(), first.IDToken) || strings.Contains(in.GetUrl(), "ann%40example.com") {
		t.Fatalf("the address carries the stored ID token or the account's email: %s", in.GetUrl())
	}
	landed := approve(t, c, in)
	if _, err := c.FinishSignIn(ctx, "ann", in.GetId(), strings.Replace(landed, "oaiapp_issued", "oaiapp_other", 1)); !errors.Is(err, ErrProviderCredential) || !strings.Contains(err.Error(), "another client") {
		t.Fatalf("a landing address naming another client, got %v", err)
	}
	if p.exchanges.Load() != 1 {
		t.Fatalf("a refused landing address spent its code: %d exchanges", p.exchanges.Load())
	}

	// Another account approves: its tokens are refused, the sign-in keeps its own, and the session it was issued is queued for revocation
	p.subject.Store("user-2")
	if _, err := signIn(t, c, "ann", cred.GetId()); !errors.Is(err, ErrProviderCredential) || !strings.Contains(err.Error(), "another account") {
		t.Fatalf("another account approving, got %v", err)
	}
	if kept := secretOf(t, c, "ann", cred.GetId()); kept != first {
		t.Fatalf("another account's tokens replaced the sign-in's: %+v", kept)
	}
	queued, err := c.db.Revocations(ctx)
	if err != nil || len(queued) != 1 || queued[0].Token == first.RefreshToken || queued[0].ClientID != "oaiapp_issued" || queued[0].Endpoint != "https://auth.openai.com/api/accounts/oauth/revoke" {
		t.Fatalf("the refused session queued for revocation %+v %v", queued, err)
	}
	c.revokeQueued(ctx)
	if got := p.revocations(); len(got) != 1 || got[0].Get("token") != queued[0].Token || got[0].Get("token_type_hint") != "refresh_token" || got[0].Get("client_id") != "oaiapp_issued" {
		t.Fatalf("revocations asked %v", got)
	}
	if queued, _ := c.db.Revocations(ctx); len(queued) != 0 {
		t.Fatalf("a revoked session stays queued %+v", queued)
	}

	// The same account approves: the tokens are replaced in place
	p.subject.Store("user-1")
	again, err := signIn(t, c, "ann", cred.GetId())
	if err != nil {
		t.Fatal(err)
	}
	next := secretOf(t, c, "ann", cred.GetId())
	if again.GetId() != cred.GetId() || next.AccessToken == first.AccessToken || next.RefreshToken == first.RefreshToken || next.ClientID != "oaiapp_issued" {
		t.Fatalf("authorized again %v %+v", again, next)
	}
	if list, _ := c.List(ctx, "ann"); len(list) != 1 {
		t.Fatalf("authorizing again made another credential: %v", list)
	}
}

// Signing keys out of reach leave the code unspent and the sign-in to be finished once they are back, and keys read
// before stand while the provider's key set is down
func TestSignInWaitsOutSigningKeysOutOfReach(t *testing.T) {
	ctx := context.Background()
	c, p := planCredentials(t)
	p.keysDown.Store(true)
	in, err := c.StartSignIn(ctx, "ann", "openai", "plan", "")
	if err != nil {
		t.Fatal(err)
	}
	landed := approve(t, c, in)
	if _, err := c.FinishSignIn(ctx, "ann", in.GetId(), landed); !errors.Is(err, ErrProviderCredential) || !strings.Contains(err.Error(), "signing keys") {
		t.Fatalf("keys out of reach, got %v", err)
	}
	if p.exchanges.Load() != 0 || p.keysRead.Load() != keysTries {
		t.Fatalf("keys out of reach: %d exchanges, %d reads", p.exchanges.Load(), p.keysRead.Load())
	}
	p.keysDown.Store(false)
	if _, err := c.FinishSignIn(ctx, "ann", in.GetId(), landed); err != nil {
		t.Fatalf("the same landing address once the keys are back: %v", err)
	}
	p.keysDown.Store(true)
	if _, err := signIn(t, c, "bob", ""); err != nil {
		t.Fatalf("a sign-in while the key set is down, its keys read before: %v", err)
	}
}

// Removing a sign-in answers at once whatever its provider does, and its session stays queued for revocation until the
// provider confirms, refuses outright, or the time a session is asked about runs out
func TestRemovedSignInIsRevokedUntilItsProviderConfirms(t *testing.T) {
	ctx := context.Background()
	c, p := planCredentials(t)
	now := time.Now()
	c.now = func() time.Time { return now }
	remove := func(status int) (string, bool) {
		t.Helper()
		cred, err := signIn(t, c, "ann", "")
		if err != nil {
			t.Fatal(err)
		}
		refresh := secretOf(t, c, "ann", cred.GetId()).RefreshToken
		p.revokeStatus.Store(int32(status))
		began := time.Now()
		unconfirmed, err := c.Delete(ctx, "ann", cred.GetId())
		if err != nil || time.Since(began) > revokeWait+2*time.Second {
			t.Fatalf("remove took %s: %v", time.Since(began), err)
		}
		if _, err := c.db.GetCredential(ctx, "ann", cred.GetId()); !db.IsNotFound(err) {
			t.Fatalf("the credential outlived its removal: %v", err)
		}
		return refresh, unconfirmed
	}
	queue := func() []db.Revocation {
		t.Helper()
		list, err := c.db.Revocations(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}

	if _, unconfirmed := remove(http.StatusOK); unconfirmed || len(queue()) != 0 {
		t.Fatalf("a revocation the provider confirmed: unconfirmed %v, queued %+v", unconfirmed, queue())
	}
	if _, unconfirmed := remove(http.StatusBadRequest); !unconfirmed || len(queue()) != 0 {
		t.Fatalf("a revocation the provider refuses outright is not asked again: unconfirmed %v, queued %+v", unconfirmed, queue())
	}

	refresh, unconfirmed := remove(http.StatusInternalServerError)
	if q := queue(); !unconfirmed || len(q) != 1 || q[0].Token != refresh || q[0].Tries != 1 {
		t.Fatalf("a revocation the provider failed: unconfirmed %v, queued %+v", unconfirmed, q)
	}
	asked := len(p.revocations())
	c.revokeQueued(ctx)
	if len(p.revocations()) != asked {
		t.Fatal("a failed revocation was asked again before its wait passed")
	}
	now = now.Add(revokeFirst + time.Second)
	c.revokeQueued(ctx)
	if q := queue(); len(p.revocations()) != asked+1 || len(q) != 1 || q[0].Tries != 2 {
		t.Fatalf("a revocation failing again: asked %d, queued %+v", len(p.revocations())-asked, q)
	}
	p.revokeStatus.Store(http.StatusOK)
	now = now.Add(2*revokeFirst + time.Second)
	c.revokeQueued(ctx)
	if got := p.revocations(); len(queue()) != 0 || got[len(got)-1].Get("token") != refresh {
		t.Fatalf("a revocation confirmed at last: queued %+v, asked %v", queue(), got[len(got)-1])
	}

	if _, unconfirmed := remove(http.StatusServiceUnavailable); !unconfirmed || len(queue()) != 1 {
		t.Fatalf("queued %+v", queue())
	}
	asked = len(p.revocations())
	now = now.Add(revokeKeep + time.Hour)
	c.revokeQueued(ctx)
	if len(queue()) != 0 || len(p.revocations()) != asked {
		t.Fatalf("a session asked about past its time: queued %+v", queue())
	}
}

// A refresh holds up neither another sign-in's nor its own request past that request's time, runs to its end all the same,
// and a provider that failed one is not asked again by every request while the access still holds
func TestRefreshWaitsOnNoOtherSignIn(t *testing.T) {
	c, p := planCredentials(t)
	ann, err := signIn(t, c, "ann", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signIn(t, c, "bob", ""); err != nil {
		t.Fatal(err)
	}
	// Within the margin of the access lapsing, so every request refreshes first
	c.now = func() time.Time { return time.Now().Add(58 * time.Minute) }
	hang := make(chan struct{})
	p.hang.Store(&hang)
	short, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	began := time.Now()
	if _, _, err := c.Headers(short, "ann", "openai", "", nil); !errors.Is(err, context.DeadlineExceeded) || time.Since(began) > 5*time.Second {
		t.Fatalf("a request behind a hung refresh waited %s: %v", time.Since(began), err)
	}
	// Another sign-in refreshes while the first one's refresh hangs
	p.hang.Store(nil)
	bobDone := make(chan error, 1)
	go func() {
		_, h, err := c.Headers(context.Background(), "bob", "openai", "", nil)
		if err == nil && h["Authorization"] == "" {
			err = errors.New("no header")
		}
		bobDone <- err
	}()
	select {
	case err := <-bobDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("another sign-in's refresh waited on the hung one")
	}
	// The hung refresh, let go, runs to its end and stores what the provider answered though its request is long gone
	before := secretOf(t, c, "ann", ann.GetId())
	close(hang)
	deadline := time.Now().Add(5 * time.Second)
	for secretOf(t, c, "ann", ann.GetId()).RefreshToken == before.RefreshToken {
		if time.Now().After(deadline) {
			t.Fatal("the refresh its request gave up on never stored its tokens")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A refresh the provider fails leaves the access standing and is not tried again by the next request
	c.now = func() time.Time { return time.Now().Add(116 * time.Minute) }
	p.refreshStatus.Store(http.StatusInternalServerError)
	tried := p.refreshes.Load()
	for range 3 {
		if _, h, err := c.Headers(context.Background(), "ann", "openai", "", nil); err != nil || h["Authorization"] == "" {
			t.Fatalf("access that still holds %v %v", h, err)
		}
	}
	if got := p.refreshes.Load() - tried; got != 1 {
		t.Fatalf("a failed refresh was tried %d times by three requests", got)
	}
}

// A credential its provider rejected is passed over for the next, and gone under all the same when nothing else is left,
// so a rejection alone never keeps every request out
func TestRejectedCredentialIsPassedOverNotShutOut(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	c, _ := clocked(t, start)
	only := keep(t, c, "lab", "only")
	pick := func() string {
		t.Helper()
		cred, _, err := c.Headers(ctx, "ann", "lab", "gpt-4o", nil)
		if err != nil {
			t.Fatal(err)
		}
		return cred.GetName()
	}
	c.Answered(only, "gpt-4o", http.StatusUnauthorized, http.Header{}, 0)
	if w := windowOf(t, c, only.GetId(), rejectedName); w == nil || !w.GetSpent() {
		t.Fatalf("the rejection shows on the credential %v", w)
	}
	if got := pick(); got != "only" {
		t.Fatalf("the one credential kept, rejected once, went under %q", got)
	}
	spare := keep(t, c, "lab", "spare")
	if got := pick(); got != "spare" {
		t.Fatalf("a rejected credential is passed over for the next, got %q", got)
	}
	c.Answered(spare, "gpt-4o", http.StatusTooManyRequests, http.Header{}, time.Minute)
	if got := pick(); got != "only" {
		t.Fatalf("with the rest spent the rejected one is gone under, got %q", got)
	}
	// The provider takes the rejected credential after all, which ends its rejection at once
	c.Answered(only, "gpt-4o", http.StatusOK, http.Header{}, 0)
	if w := windowOf(t, c, only.GetId(), rejectedName); w != nil {
		t.Fatalf("a credential its provider took again stays rejected: %v", w)
	}
	c.Answered(only, "gpt-4o", http.StatusUnauthorized, http.Header{}, 0)
	c.Answered(only, "gpt-4o", http.StatusTooManyRequests, http.Header{}, 30*time.Second)
	if _, _, err := c.Headers(ctx, "ann", "lab", "gpt-4o", nil); err == nil {
		t.Fatal("a credential over its limits as well as rejected is spent")
	} else if se, ok := errors.AsType[*requestcontrol.SpentError](err); !ok || se.Retry <= 0 {
		t.Fatalf("every credential spent, got %v", err)
	}
}

// A rolling span stored before spans were bounded reads as the nearest one usage can measure, and none outside is saved
func TestRollingSpanReadsWithinWhatUsageMeasures(t *testing.T) {
	for seconds, want := range map[uint32]time.Duration{1: 300 * time.Second, 300: 300 * time.Second, 3600: time.Hour, 4_000_000_000: usageKeep} {
		if got := rolling(&v1.CredentialBudget{Seconds: seconds}); got != want {
			t.Fatalf("%d seconds roll over %s, want %s", seconds, got, want)
		}
	}
	for _, seconds := range []uint32{60, 8_640_001} {
		b := &v1.CredentialBudget{Name: "b", Unit: v1.BudgetUnit_BUDGET_UNIT_TOKENS, Limit: 1, Period: v1.BudgetPeriod_BUDGET_PERIOD_ROLLING, Seconds: seconds}
		if err := checkBudgets([]*v1.CredentialBudget{b}); !errors.Is(err, ErrProviderCredential) {
			t.Fatalf("a span of %d seconds saved: %v", seconds, err)
		}
	}
}

// A member names the owner whose provider credentials sign a pull it sends on, and reaches no owner it names on any other call
func TestMemberNamesAnOwnerOnAPullAlone(t *testing.T) {
	guard := NewGuard("daemon-token", nil, nil)
	guard.SetPeers(onePeer{token: "member-session", node: "peer"})
	member := http.Header{"Authorization": {"Bearer member-session"}, CredentialOwnerHeader: {"u1"}}
	if got := guard.CallOwner(nebuv1connect.MeshServicePullSeatProcedure, member); got != "u1" {
		t.Fatalf("a pull a member sends on goes under %q", got)
	}
	for _, procedure := range []string{nebuv1connect.SourceServiceSearchProcedure, nebuv1connect.SourceServiceResolveProcedure, nebuv1connect.MeshServiceRunSeatProcedure, nebuv1connect.ToolServiceCallToolProcedure} {
		if got := guard.CallOwner(procedure, member); got != ProviderDaemon {
			t.Fatalf("%s from a member naming an owner goes under %q", procedure, got)
		}
	}
	if got := guard.CredentialOwner(member); got != ProviderDaemon {
		t.Fatalf("a member naming an owner outside a call goes under %q", got)
	}
	// No one but a member is taken at its word
	daemon := http.Header{"Authorization": {"Bearer daemon-token"}, CredentialOwnerHeader: {"u1"}}
	if got := guard.CallOwner(nebuv1connect.MeshServicePullSeatProcedure, daemon); got != ProviderDaemon {
		t.Fatalf("the daemon token naming an owner goes under %q", got)
	}
	if got := guard.CallOwner(nebuv1connect.MeshServicePullSeatProcedure, http.Header{CredentialOwnerHeader: {"u1"}}); got != "" {
		t.Fatalf("a call no credential opens goes under %q", got)
	}
}
