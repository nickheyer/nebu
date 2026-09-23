package sso

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	discoveryPath = "/.well-known/openid-configuration"
	// Largest provider response read
	maxResponse = 1 << 20
	// Minimum gap between key set refreshes for unknown key ids
	jwksRefreshGap = time.Minute
	clockSkew      = 5 * time.Minute
)

// The provider's discovery document
type discovery struct {
	Issuer        string   `json:"issuer"`
	Authorization string   `json:"authorization_endpoint"`
	Token         string   `json:"token_endpoint"`
	Userinfo      string   `json:"userinfo_endpoint"`
	JWKS          string   `json:"jwks_uri"`
	TokenAuth     []string `json:"token_endpoint_auth_methods_supported"`
	PKCE          []string `json:"code_challenge_methods_supported"`
}

// Discovery and the signing keys it points at
type provider struct {
	doc     discovery
	keys    []jwk
	fetched time.Time
}

// The token endpoint's answer to a code exchange
type tokenResponse struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// Fetches and caches the discovery document
func (s *Service) provider(ctx context.Context) (*provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prov != nil {
		return s.prov, nil
	}
	issuer := strings.TrimSuffix(s.cfg.GetIssuer(), "/")
	var doc discovery
	if err := s.getJSON(ctx, issuer+discoveryPath, "", &doc); err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	if strings.TrimSuffix(doc.Issuer, "/") != issuer {
		return nil, fmt.Errorf("discovery names issuer %q, auth.oidc.issuer is %q", doc.Issuer, s.cfg.GetIssuer())
	}
	if doc.Authorization == "" || doc.Token == "" {
		return nil, errors.New("discovery lacks authorization_endpoint or token_endpoint")
	}
	s.prov = &provider{doc: doc}
	return s.prov, nil
}

// Returns the signing keys, fetching them on first use or when asked to refresh
func (s *Service) keys(ctx context.Context, p *provider, refresh bool) ([]jwk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fresh := s.now().Sub(p.fetched) < jwksRefreshGap
	if p.keys != nil && (!refresh || fresh) {
		return p.keys, nil
	}
	if p.doc.JWKS == "" {
		return nil, errors.New("discovery lacks jwks_uri")
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := s.getJSON(ctx, p.doc.JWKS, "", &set); err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	p.keys, p.fetched = set.Keys, s.now()
	return p.keys, nil
}

// Fetches JSON, with a bearer token when given
func (s *Service) getJSON(ctx context.Context, u, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s: %s", u, resp.Status, excerpt(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: %w", u, err)
	}
	return nil
}

// Picks how to present the client secret, as the provider advertises
func clientAuth(supported []string, secret string) (string, error) {
	if secret == "" {
		return "none", nil
	}
	if len(supported) == 0 || slices.Contains(supported, "client_secret_basic") {
		return "client_secret_basic", nil
	}
	if slices.Contains(supported, "client_secret_post") {
		return "client_secret_post", nil
	}
	return "", fmt.Errorf("the provider authenticates clients with %s, nebu sends client_secret_basic or client_secret_post", strings.Join(supported, ", "))
}

// Trades the authorization code for tokens
func (s *Service) exchange(ctx context.Context, p *provider, code, redirect, verifier string) (*tokenResponse, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {s.cfg.GetClientId()}}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	method, err := clientAuth(p.doc.TokenAuth, s.cfg.GetClientSecret())
	if err != nil {
		return nil, err
	}
	if method == "client_secret_post" {
		form.Set("client_secret", s.cfg.GetClientSecret())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.doc.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if method == "client_secret_basic" {
		req.SetBasicAuth(url.QueryEscape(s.cfg.GetClientId()), url.QueryEscape(s.cfg.GetClientSecret()))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, err
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("token endpoint returned %s: %s", resp.Status, excerpt(body))
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("token endpoint refused: %s %s", tr.Error, tr.Description)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %s: %s", resp.Status, excerpt(body))
	}
	if tr.IDToken == "" {
		return nil, errors.New("token endpoint sent no id_token, the client must be allowed the openid scope")
	}
	return &tr, nil
}

// Checks the ID token's signature and standard claims
func (s *Service) verifyIDToken(ctx context.Context, p *provider, raw, nonce string) (map[string]any, error) {
	tok, err := parseToken(raw)
	if err != nil {
		return nil, fmt.Errorf("id token: %w", err)
	}
	if err := s.verifySignature(ctx, p, tok); err != nil {
		return nil, fmt.Errorf("id token signature: %w", err)
	}
	c := tok.claims
	if iss := claimString(c, "iss"); strings.TrimSuffix(iss, "/") != strings.TrimSuffix(p.doc.Issuer, "/") {
		return nil, fmt.Errorf("id token issuer %q is not %q", iss, p.doc.Issuer)
	}
	if !slices.Contains(claimStrings(c["aud"]), s.cfg.GetClientId()) {
		return nil, fmt.Errorf("id token audience %v is not client %q", claimStrings(c["aud"]), s.cfg.GetClientId())
	}
	if azp := claimString(c, "azp"); azp != "" && azp != s.cfg.GetClientId() {
		return nil, fmt.Errorf("id token authorized party %q is not client %q", azp, s.cfg.GetClientId())
	}
	now := s.now()
	exp, ok := claimTime(c, "exp")
	if !ok {
		return nil, errors.New("id token has no expiry")
	}
	if now.After(exp.Add(clockSkew)) {
		return nil, fmt.Errorf("id token expired at %s", exp.UTC().Format(time.RFC3339))
	}
	if nbf, ok := claimTime(c, "nbf"); ok && now.Before(nbf.Add(-clockSkew)) {
		return nil, fmt.Errorf("id token is not valid before %s", nbf.UTC().Format(time.RFC3339))
	}
	if iat, ok := claimTime(c, "iat"); ok && iat.After(now.Add(clockSkew)) {
		return nil, fmt.Errorf("id token issued in the future at %s", iat.UTC().Format(time.RFC3339))
	}
	if got := claimString(c, "nonce"); got != nonce {
		return nil, errors.New("id token nonce does not match this sign-in")
	}
	if claimString(c, "sub") == "" {
		return nil, errors.New("id token has no subject")
	}
	return c, nil
}

// Verifies with the client secret or the published keys, refreshing them once for an unknown key id
func (s *Service) verifySignature(ctx context.Context, p *provider, tok *token) error {
	if strings.HasPrefix(tok.alg, "HS") {
		return tok.verify(nil, s.cfg.GetClientSecret())
	}
	keys, err := s.keys(ctx, p, false)
	if err != nil {
		return err
	}
	err = tok.verify(keys, "")
	if !errors.Is(err, errUnknownKey) {
		return err
	}
	if keys, err = s.keys(ctx, p, true); err != nil {
		return err
	}
	return tok.verify(keys, "")
}

// Fetches userinfo claims, verifying them when the provider signs the response
func (s *Service) userinfo(ctx context.Context, p *provider, accessToken string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.doc.Userinfo, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json, application/jwt")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo returned %s: %s", resp.Status, excerpt(body))
	}
	kind, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if kind == "application/jwt" {
		tok, err := parseToken(string(body))
		if err != nil {
			return nil, fmt.Errorf("userinfo: %w", err)
		}
		if err := s.verifySignature(ctx, p, tok); err != nil {
			return nil, fmt.Errorf("userinfo signature: %w", err)
		}
		return tok.claims, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	claims := map[string]any{}
	if err := dec.Decode(&claims); err != nil {
		return nil, fmt.Errorf("userinfo: %w", err)
	}
	return claims, nil
}

// The S256 challenge for a PKCE verifier
func challenge(verifier string) string {
	sum := sha256Sum([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// A short, single line view of a response body for errors
func excerpt(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
