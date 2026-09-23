package sso

import (
	"crypto/subtle"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
)

const (
	loginPath    = "/auth/oidc/login"
	callbackPath = "/auth/oidc/callback"
	loginCookie  = "nebu_login"
	// Longest a sign-in may take between leaving for the provider and returning
	loginTTL = 10 * time.Minute
	// Bytes of entropy in state, nonce, and PKCE verifier
	randomBytes = 32
)

// Sign-in in progress, sealed in a short lived cookie
type loginState struct {
	State    string    `json:"state"`
	Nonce    string    `json:"nonce"`
	Verifier string    `json:"verifier,omitempty"`
	Next     string    `json:"next"`
	Expires  time.Time `json:"exp"`
}

// Serves sign-in and the provider callback under /auth/oidc/
func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+loginPath, s.login)
	mux.HandleFunc("GET "+callbackPath, s.callback)
	return mux
}

// Sends the browser to the provider with state, nonce, and a PKCE challenge
func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	p, err := s.provider(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadGateway, "The sign-in provider could not be reached.", err)
		return
	}
	state, err := auth.RandomString(randomBytes)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, "Could not start sign-in.", err)
		return
	}
	nonce, _ := auth.RandomString(randomBytes)
	st := loginState{State: state, Nonce: nonce, Next: safeNext(r.URL.Query().Get("next")), Expires: s.now().Add(loginTTL)}
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {s.cfg.GetClientId()},
		"redirect_uri":  {s.redirectURI(r)},
		"scope":         {strings.Join(s.scopes, " ")},
		"state":         {state},
		"nonce":         {nonce},
	}
	// Providers that list their PKCE methods without S256 get none, everyone else gets S256.
	if len(p.doc.PKCE) == 0 || slices.Contains(p.doc.PKCE, "S256") {
		st.Verifier, _ = auth.RandomString(randomBytes)
		q.Set("code_challenge", challenge(st.Verifier))
		q.Set("code_challenge_method", "S256")
	}
	value, err := s.sessions.Seal(loginCookie, st)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, "Could not start sign-in.", err)
		return
	}
	auth.SetCookie(w, r, auth.NewCookie(loginCookie, value, loginTTL))
	sep := "?"
	if strings.Contains(p.doc.Authorization, "?") {
		sep = "&"
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, p.doc.Authorization+sep+q.Encode(), http.StatusFound)
}

// Finishes sign-in when the provider sends the browser back
func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(loginCookie)
	if err != nil {
		s.fail(w, http.StatusBadRequest, "No sign-in is in progress in this browser. Start again.", err)
		return
	}
	auth.ClearCookie(w, r, loginCookie)
	var st loginState
	if err := s.sessions.Open(loginCookie, c.Value, &st); err != nil {
		s.fail(w, http.StatusBadRequest, "The sign-in cookie is not one this daemon issued. Start again.", err)
		return
	}
	if s.now().After(st.Expires) {
		s.fail(w, http.StatusBadRequest, "Sign-in took too long. Start again.", errors.New("login state expired"))
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		s.fail(w, http.StatusForbidden, "The provider refused the sign-in.", errors.New(strings.TrimSpace(e+" "+q.Get("error_description"))))
		return
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		s.fail(w, http.StatusBadRequest, "The provider answered a different sign-in. Start again.", errors.New("state mismatch"))
		return
	}
	code := q.Get("code")
	if code == "" {
		s.fail(w, http.StatusBadRequest, "The provider sent no authorization code.", errors.New("missing code"))
		return
	}
	ctx := r.Context()
	p, err := s.provider(ctx)
	if err != nil {
		s.fail(w, http.StatusBadGateway, "The sign-in provider could not be reached.", err)
		return
	}
	tr, err := s.exchange(ctx, p, code, s.redirectURI(r), st.Verifier)
	if err != nil {
		s.fail(w, http.StatusBadGateway, "The provider did not accept the authorization code.", err)
		return
	}
	claims, err := s.verifyIDToken(ctx, p, tr.IDToken, st.Nonce)
	if err != nil {
		s.fail(w, http.StatusUnauthorized, "The provider's identity token was rejected.", err)
		return
	}
	id := s.identityFrom(claims)
	if s.needsUserinfo(id) && p.doc.Userinfo != "" && tr.AccessToken != "" {
		extra, err := s.userinfo(ctx, p, tr.AccessToken)
		if err != nil {
			s.fail(w, http.StatusBadGateway, "The provider's userinfo endpoint failed.", err)
			return
		}
		if sub := claimString(extra, "sub"); sub != "" && sub != id.subject {
			s.fail(w, http.StatusUnauthorized, "The provider's userinfo names a different subject.", errors.New("userinfo subject mismatch"))
			return
		}
		id = s.identityFrom(merge(claims, extra))
	}
	if err := s.allowed(id); err != nil {
		s.fail(w, http.StatusForbidden, "This account is not allowed to use nebu.", err)
		return
	}
	if _, err := s.sessions.Issue(w, r, auth.Session{Subject: id.subject, Email: id.email, Name: id.name, Provider: auth.ProviderOIDC}); err != nil {
		s.fail(w, http.StatusInternalServerError, "Could not create the session.", err)
		return
	}
	s.log.Info("signed in", "subject", id.subject, "email", id.email, "name", id.name)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, st.Next, http.StatusSeeOther)
}

// The callback URL registered with the provider, from public_url or the request
func (s *Service) redirectURI(r *http.Request) string {
	base := strings.TrimSuffix(s.cfg.GetPublicUrl(), "/")
	if base == "" {
		scheme := "http"
		if auth.Secure(r) {
			scheme = "https"
		}
		host := r.Header.Get("X-Forwarded-Host")
		if host == "" {
			host = r.Host
		}
		base = scheme + "://" + host
	}
	return base + callbackPath
}

// Keeps redirects inside the app
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

var failPage = template.Must(template.New("fail").Parse(`<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>nebu sign-in</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0b0c10;color:#e6e6e6;font:15px/1.5 system-ui,sans-serif}
main{max-width:34rem;padding:2rem}
h1{font-size:1.25rem;margin:0 0 .5rem}
pre{white-space:pre-wrap;word-break:break-word;background:#15171c;border:1px solid #2a2d35;border-radius:6px;padding:.75rem;color:#b8bcc6;font-size:13px}
a{color:#8ab4ff}
</style>
<main>
<h1>Sign-in failed</h1>
<p>{{.Message}}</p>
<pre>{{.Detail}}</pre>
<p><a href="/auth/oidc/login">Try again</a> · <a href="/">Back to nebu</a></p>
</main>
`))

// Logs the failure and shows the browser a page it can act on
func (s *Service) fail(w http.ResponseWriter, code int, message string, err error) {
	s.log.Warn("sign-in failed", "status", code, "reason", message, "err", err)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	failPage.Execute(w, map[string]string{"Message": message, "Detail": err.Error()})
}
