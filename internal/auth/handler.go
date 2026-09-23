package auth

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"time"
)

const (
	// Path prefix the handler serves
	BasePath = "/auth/"
	// Prefix single sign-on routes live under
	SSOPath = "/auth/oidc/"
	// Largest sign-in request body
	maxBody = 16 << 10
)

// What the handler serves with. Nil Sessions means authentication is off.
type Options struct {
	Sessions *Sessions
	// Local accounts, nil when single sign-on replaces them or authentication is off
	Users *Users
	// Single sign-on provider name and its routes, empty and nil when off
	SSO        string
	SSOHandler http.Handler
	Log        *slog.Logger
}

// What the UI needs to offer sign-in and show who is signed in
type status struct {
	Enabled bool       `json:"enabled"`
	SSO     *ssoInfo   `json:"sso"`
	Local   *localInfo `json:"local"`
	User    *userInfo  `json:"user"`
}

type ssoInfo struct {
	Name string `json:"name"`
}

type localInfo struct {
	// No account exists yet, the next visitor creates the first
	Setup bool `json:"setup"`
}

type userInfo struct {
	Subject  string    `json:"subject"`
	Email    string    `json:"email"`
	Name     string    `json:"name"`
	Provider string    `json:"provider"`
	Expires  time.Time `json:"expires"`
}

// A sign-in or setup request
type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Serves session lookup, sign-out, local sign-in and setup, and single sign-on under /auth/
func Handler(o Options) http.Handler {
	h := &handler{Options: o}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/session", h.status)
	mux.HandleFunc("POST /auth/logout", h.logout)
	mux.HandleFunc("POST /auth/local/login", h.login)
	mux.HandleFunc("POST /auth/local/setup", h.setup)
	if o.SSOHandler != nil {
		mux.Handle(SSOPath, o.SSOHandler)
	} else {
		mux.HandleFunc(SSOPath, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "single sign-on is not configured, set auth.oidc in the daemon config", http.StatusNotFound)
		})
	}
	return mux
}

type handler struct {
	Options
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	out := status{Enabled: h.Sessions != nil}
	if h.SSO != "" {
		out.SSO = &ssoInfo{Name: h.SSO}
	}
	if h.Users != nil {
		out.Local = &localInfo{Setup: h.Users.Count() == 0}
	}
	if h.Sessions != nil {
		if sess, ok := h.Sessions.Session(r.Header); ok {
			out.User = &userInfo{Subject: sess.Subject, Email: sess.Email, Name: sess.Name, Provider: sess.Provider, Expires: sess.Expires}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if h.Sessions != nil {
		h.Sessions.Clear(w, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

// Signs in with a local account
func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	if h.Users == nil {
		h.refuse(w, http.StatusNotFound, errors.New("local accounts are off"))
		return
	}
	c, ok := h.readCredentials(w, r)
	if !ok {
		return
	}
	user, err := h.Users.Login(r.Context(), clientAddr(r), c.Username, c.Password)
	switch {
	case errors.Is(err, ErrThrottled):
		w.Header().Set("Retry-After", "60")
		h.refuse(w, http.StatusTooManyRequests, err)
		return
	case errors.Is(err, ErrCredentials):
		h.refuse(w, http.StatusUnauthorized, err)
		return
	case err != nil:
		h.Log.Error("sign-in failed", "err", err)
		h.refuse(w, http.StatusInternalServerError, errors.New("sign-in failed, see the daemon log"))
		return
	}
	sess, err := h.Sessions.Issue(w, r, Session{Subject: user.GetId(), Name: user.GetUsername(), Provider: ProviderLocal})
	if err != nil {
		h.Log.Error("session failed", "err", err)
		h.refuse(w, http.StatusInternalServerError, errors.New("could not start the session"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": userInfo{Subject: sess.Subject, Name: sess.Name, Provider: sess.Provider, Expires: sess.Expires}})
}

// Creates the first account and signs it in
func (h *handler) setup(w http.ResponseWriter, r *http.Request) {
	if h.Users == nil {
		h.refuse(w, http.StatusNotFound, errors.New("local accounts are off"))
		return
	}
	if h.Users.Count() > 0 {
		h.refuse(w, http.StatusConflict, errors.New("an account already exists, sign in instead"))
		return
	}
	c, ok := h.readCredentials(w, r)
	if !ok {
		return
	}
	user, err := h.Users.Create(r.Context(), c.Username, c.Password)
	switch {
	case errors.Is(err, ErrUser):
		h.refuse(w, http.StatusBadRequest, err)
		return
	case err != nil:
		h.Log.Error("setup failed", "err", err)
		h.refuse(w, http.StatusInternalServerError, errors.New("setup failed, see the daemon log"))
		return
	}
	sess, err := h.Sessions.Issue(w, r, Session{Subject: user.GetId(), Name: user.GetUsername(), Provider: ProviderLocal})
	if err != nil {
		h.Log.Error("session failed", "err", err)
		h.refuse(w, http.StatusInternalServerError, errors.New("could not start the session"))
		return
	}
	h.Log.Info("first account created", "username", user.GetUsername(), "client", clientAddr(r))
	writeJSON(w, http.StatusOK, map[string]any{"user": userInfo{Subject: sess.Subject, Name: sess.Name, Provider: sess.Provider, Expires: sess.Expires}})
}

// Reads a JSON body. The content type requirement keeps HTML forms on other sites from posting here.
func (h *handler) readCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if kind != "application/json" {
		h.refuse(w, http.StatusUnsupportedMediaType, errors.New("send application/json"))
		return credentials{}, false
	}
	var c credentials
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&c); err != nil {
		h.refuse(w, http.StatusBadRequest, errors.New("body must be JSON with username and password"))
		return credentials{}, false
	}
	return c, true
}

func (h *handler) refuse(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// The client's address without its port, for throttling
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
