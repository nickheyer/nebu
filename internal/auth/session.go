// Package auth guards the API listener and web UI: sealed browser sessions,
// local accounts, and the API token.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/db"
)

const (
	sessionCookie = "nebu_session"
	keyBytes      = 32
	// Where a session came from
	ProviderLocal = db.LocalProvider
	ProviderOIDC  = "oidc"
)

// Who signed in, as kept in the browser session
type Session struct {
	Subject  string    `json:"sub"`
	Email    string    `json:"email,omitempty"`
	Name     string    `json:"name,omitempty"`
	Provider string    `json:"prv"`
	Issued   time.Time `json:"iat"`
	Expires  time.Time `json:"exp"`
}

// Seals browser cookies with a key kept in the data directory, so sessions outlive restarts
type Sessions struct {
	aead cipher.AEAD
	ttl  time.Duration
	now  func() time.Time

	mu sync.RWMutex
	// Extra checks a session must pass, such as its account still existing
	validators []func(*Session) bool
}

// Loads the cookie key, creating it on first use
func OpenSessions(keyFile string, ttl time.Duration) (*Sessions, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("session lifetime %s must be positive", ttl)
	}
	key, err := os.ReadFile(keyFile)
	if errors.Is(err, fs.ErrNotExist) {
		key = make([]byte, keyBytes)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(keyFile, key, 0o600); err != nil {
			return nil, fmt.Errorf("session key: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("session key: %w", err)
	}
	if len(key) != keyBytes {
		return nil, fmt.Errorf("session key %s must hold %d bytes, delete it to make a new one", keyFile, keyBytes)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sessions{aead: aead, ttl: ttl, now: time.Now}, nil
}

// How long a new session lasts
func (s *Sessions) TTL() time.Duration { return s.ttl }

// Adds a check every session must pass, such as its account still existing
func (s *Sessions) Validate(fn func(*Session) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.validators = append(s.validators, fn)
}

// Starts a session for the browser, stamping its lifetime
func (s *Sessions) Issue(w http.ResponseWriter, r *http.Request, sess Session) (*Session, error) {
	sess.Issued = s.now()
	sess.Expires = sess.Issued.Add(s.ttl)
	c, err := s.Cookie(sess)
	if err != nil {
		return nil, err
	}
	SetCookie(w, r, c)
	return &sess, nil
}

// Seals a session into the cookie the API and gateway accept
func (s *Sessions) Cookie(sess Session) (*http.Cookie, error) {
	value, err := s.Seal(sessionCookie, sess)
	if err != nil {
		return nil, err
	}
	return NewCookie(sessionCookie, value, sess.Expires.Sub(s.now())), nil
}

// Ends the browser's session
func (s *Sessions) Clear(w http.ResponseWriter, r *http.Request) {
	ClearCookie(w, r, sessionCookie)
}

// Reports whether the headers carry a live session
func (s *Sessions) Authenticated(h http.Header) bool {
	_, ok := s.Session(h)
	return ok
}

// Reads the session cookie from request headers
func (s *Sessions) Session(h http.Header) (*Session, bool) {
	req := http.Request{Header: h}
	c, err := req.Cookie(sessionCookie)
	if err != nil {
		return nil, false
	}
	var sess Session
	if err := s.Open(sessionCookie, c.Value, &sess); err != nil {
		return nil, false
	}
	if !s.now().Before(sess.Expires) {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ok := range s.validators {
		if !ok(&sess) {
			return nil, false
		}
	}
	return &sess, true
}

// Encrypts a value for a cookie. The purpose binds the value to one cookie name.
func (s *Sessions) Seal(purpose string, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, plain, []byte(purpose))
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// Decrypts a cookie value sealed for the same purpose
func (s *Sessions) Open(purpose, value string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return err
	}
	size := s.aead.NonceSize()
	if len(raw) < size {
		return errors.New("cookie too short")
	}
	plain, err := s.aead.Open(nil, raw[:size], raw[size:], []byte(purpose))
	if err != nil {
		return errors.New("cookie does not verify")
	}
	return json.Unmarshal(plain, v)
}

// Reports whether the browser reached the daemon over HTTPS, directly or through a proxy
func Secure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// A browser-only cookie scoped to the whole app
func NewCookie(name, value string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: int(ttl / time.Second), HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

// Sends the cookie, marking it secure when the browser used HTTPS
func SetCookie(w http.ResponseWriter, r *http.Request, c *http.Cookie) {
	c.Secure = Secure(r)
	http.SetCookie(w, c)
}

func ClearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: Secure(r), SameSite: http.SameSiteLaxMode})
}

// A random URL safe string with n bytes of entropy
func RandomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
