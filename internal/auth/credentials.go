package auth

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxCredentialName = 64
	// Credentials one owner may keep
	maxCredentials = 100
	// How long before a sign-in's access lapses its tokens are refreshed
	refreshMargin = 5 * time.Minute
	// How long a sign-in by redirect waits for the address the person copies back
	redirectTTL = 10 * time.Minute
)

var (
	// Invalid credential request
	ErrProviderCredential = errors.New("provider credential")
	// No such credential or sign-in among the caller's
	ErrUnknownCredential = errors.New("unknown credential")
)

// Credentials callers keep for providers serving models, each owner's their own
type Credentials struct {
	db      *db.DB
	sources *sources.Registry
	log     *slog.Logger
	client  *http.Client
	now     func() time.Time
	// This daemon's host id, which a sign-in naming its host carries
	host string

	mu      sync.Mutex
	pending map[string]*pendingSignIn
	// One refresh at a time, since a provider may take each refresh token once
	refreshMu sync.Mutex
}

// A sign-in the person has yet to approve, by device or by the address the provider sends the browser to
type pendingSignIn struct {
	owner, sourceID, name string
	expires               time.Time
	device                *sources.Device
	state, verifier       string
}

// A sign-in's tokens as the secret column keeps them, with the client a provider issued the sign-in
type signedIn struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token,omitempty"`
	Expiry       time.Time `json:"expiry"`
	ClientID     string    `json:"client_id,omitempty"`
}

func NewCredentials(store *db.DB, srcs *sources.Registry, log *slog.Logger, host string) *Credentials {
	return &Credentials{db: store, sources: srcs, log: log, client: &http.Client{Timeout: time.Minute}, now: time.Now, host: host, pending: map[string]*pendingSignIn{}}
}

// The owner a caller's provider credentials are kept under, its trace owner, the daemon token and auth.disabled sharing one
func CredentialOwner(id Identity) string { return cmp.Or(id.Owner(), ProviderDaemon) }

// The source and its provider's catalog, nil when the source serves no models
func (c *Credentials) provider(sourceID string) (sources.Source, *sources.Catalog, error) {
	if sourceID == "" {
		return nil, nil, fmt.Errorf("%w: name the source", ErrProviderCredential)
	}
	src, err := c.sources.Get(sourceID)
	if err != nil {
		return nil, nil, err
	}
	return src, sources.ProviderOf(src.Spec().GetKind()), nil
}

// The name a source goes by in messages
func sourceName(src sources.Source, cat *sources.Catalog) string {
	if name := src.Spec().GetName(); name != "" {
		return name
	}
	return cat.Name
}

// The owner's credentials by source and position, never with a secret
func (c *Credentials) List(ctx context.Context, owner string) ([]*v1.ProviderCredential, error) {
	rows, err := c.db.ListCredentials(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := make([]*v1.ProviderCredential, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Credential)
	}
	return out, nil
}

// Stores a key as the owner's, a new credential when the request names no id, an empty secret or name keeping the stored one
func (c *Credentials) Put(ctx context.Context, owner string, in *v1.PutCredentialRequest) (*v1.ProviderCredential, error) {
	name, secret := strings.TrimSpace(in.GetName()), strings.TrimSpace(in.GetSecret())
	if len(name) > maxCredentialName {
		return nil, fmt.Errorf("%w: name is longer than %d characters", ErrProviderCredential, maxCredentialName)
	}
	var row db.CredentialRow
	if in.GetId() == "" {
		src, cat, err := c.provider(in.GetSourceId())
		if err != nil {
			return nil, err
		}
		switch {
		case cat == nil:
			return nil, fmt.Errorf("%w: source %s serves no models", ErrProviderCredential, in.GetSourceId())
		case !cat.SignIn.Key:
			return nil, fmt.Errorf("%w: %s is signed in to, start a sign-in instead", ErrProviderCredential, sourceName(src, cat))
		case secret == "":
			return nil, fmt.Errorf("%w: the key is required", ErrProviderCredential)
		}
		if row, err = c.newRow(ctx, owner, in.GetSourceId(), cmp.Or(name, sourceName(src, cat)), v1.CredentialKind_CREDENTIAL_KIND_KEY); err != nil {
			return nil, err
		}
		row.Secret = secret
	} else {
		var err error
		if row, err = c.get(ctx, owner, in.GetId()); err != nil {
			return nil, err
		}
		if secret != "" && row.Credential.GetKind() != v1.CredentialKind_CREDENTIAL_KIND_KEY {
			return nil, fmt.Errorf("%w: a sign-in's tokens come from signing in again", ErrProviderCredential)
		}
		if in.GetSourceId() != "" && in.GetSourceId() != row.Credential.GetSourceId() {
			return nil, fmt.Errorf("%w: the source of %s is fixed", ErrProviderCredential, in.GetId())
		}
		row.Credential.Name, row.Secret = cmp.Or(name, row.Credential.GetName()), cmp.Or(secret, row.Secret)
	}
	if in.GetPosition() > 0 {
		row.Credential.Position = in.GetPosition()
	}
	row.Credential.UpdatedAt = timestamppb.New(c.now().UTC())
	if err := c.save(ctx, row); err != nil {
		return nil, err
	}
	c.log.Info("provider credential stored", "owner", owner, "source", row.Credential.GetSourceId(), "id", row.Credential.GetId())
	return c.reread(ctx, owner, row.Credential.GetId())
}

// A new row placed after the owner's last credential for the source, refusing an owner holding the most allowed
func (c *Credentials) newRow(ctx context.Context, owner, sourceID, name string, kind v1.CredentialKind) (db.CredentialRow, error) {
	all, err := c.db.ListCredentials(ctx, owner)
	if err != nil {
		return db.CredentialRow{}, err
	}
	if len(all) >= maxCredentials {
		return db.CredentialRow{}, fmt.Errorf("%w: %d credentials are kept already, remove one first", ErrProviderCredential, len(all))
	}
	var position uint32
	for _, r := range all {
		if r.Credential.GetSourceId() == sourceID {
			position = max(position, r.Credential.GetPosition())
		}
	}
	now := timestamppb.New(c.now().UTC())
	return db.CredentialRow{Owner: owner, Credential: &v1.ProviderCredential{Id: db.NewID(), SourceId: sourceID, Name: name, Kind: kind, Position: position + 1, CreatedAt: now, UpdatedAt: now}}, nil
}

func (c *Credentials) get(ctx context.Context, owner, id string) (db.CredentialRow, error) {
	row, err := c.db.GetCredential(ctx, owner, id)
	if db.IsNotFound(err) {
		return row, fmt.Errorf("%w: %s", ErrUnknownCredential, id)
	}
	return row, err
}

func (c *Credentials) reread(ctx context.Context, owner, id string) (*v1.ProviderCredential, error) {
	row, err := c.get(ctx, owner, id)
	return row.Credential, err
}

func (c *Credentials) save(ctx context.Context, row db.CredentialRow) error {
	written, err := c.db.PutCredential(ctx, row)
	if err == nil && !written {
		err = fmt.Errorf("%w: %s", ErrUnknownCredential, row.Credential.GetId())
	}
	return err
}

// Removes one of the owner's credentials
func (c *Credentials) Delete(ctx context.Context, owner, id string) error {
	ok, err := c.db.DeleteCredential(ctx, owner, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownCredential, id)
	}
	c.log.Info("provider credential removed", "owner", owner, "id", id)
	return nil
}

// Random characters for a state or a nonce
func random() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Starts a sign-in at the provider: a code the person enters, or an address the person approves at and copies the landing address back from
func (c *Credentials) StartSignIn(ctx context.Context, owner, sourceID, name string) (*v1.ProviderSignIn, error) {
	src, cat, err := c.provider(sourceID)
	if err != nil {
		return nil, err
	}
	if cat == nil || cat.SignIn.OAuth == nil {
		return nil, fmt.Errorf("%w: source %s takes no sign-in", ErrProviderCredential, sourceID)
	}
	o := cat.SignIn.OAuth
	p := &pendingSignIn{owner: owner, sourceID: sourceID, name: cmp.Or(strings.TrimSpace(name), sourceName(src, cat))}
	out := &v1.ProviderSignIn{SourceId: sourceID}
	if o.Device != nil {
		if p.device, err = o.Device(ctx, c.client); err != nil {
			return nil, fmt.Errorf("sign-in with %s: %w", sourceName(src, cat), err)
		}
		p.expires, out.Url, out.Code = p.device.Expires, p.device.URL, p.device.Code
	} else {
		p.state, p.verifier, p.expires = random(), oauth2.GenerateVerifier(), c.now().Add(redirectTTL)
		opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(p.verifier)}
		for k, v := range o.Authorize {
			opts = append(opts, oauth2.SetAuthURLParam(k, strings.NewReplacer("{host}", c.host, "{nonce}", random()).Replace(v)))
		}
		out.Url = o.Config.AuthCodeURL(p.state, opts...)
	}
	out.Id, out.ExpiresAt = db.NewID(), timestamppb.New(p.expires)
	c.mu.Lock()
	now := c.now()
	for k, old := range c.pending {
		if now.After(old.expires) {
			delete(c.pending, k)
		}
	}
	c.pending[out.Id] = p
	c.mu.Unlock()
	return out, nil
}

// Completes a sign-in of the owner's, waiting for a device's approval or reading the landing address copied back, and keeps the credential it yields
func (c *Credentials) FinishSignIn(ctx context.Context, owner, id, callback string) (*v1.ProviderCredential, error) {
	c.mu.Lock()
	p, ok := c.pending[id]
	c.mu.Unlock()
	if !ok || p.owner != owner || c.now().After(p.expires) {
		return nil, fmt.Errorf("%w: sign-in %s, start another", ErrUnknownCredential, id)
	}
	_, cat, err := c.provider(p.sourceID)
	if err != nil || cat == nil || cat.SignIn.OAuth == nil {
		return nil, fmt.Errorf("%w: source %s takes no sign-in", ErrProviderCredential, p.sourceID)
	}
	o := cat.SignIn.OAuth
	kind := v1.CredentialKind_CREDENTIAL_KIND_SIGN_IN
	var tok *oauth2.Token
	var key, clientID string
	if p.device != nil {
		if tok, err = p.device.Wait(ctx); err != nil {
			if ctx.Err() == nil {
				c.forget(id)
			}
			return nil, err
		}
	} else {
		code, state, issued, err := landing(callback)
		switch {
		case err != nil:
			return nil, err
		case state != "" && state != p.state:
			return nil, fmt.Errorf("%w: the address belongs to another sign-in", ErrProviderCredential)
		case o.Issues && issued == "":
			return nil, fmt.Errorf("%w: the address names no client, copy the whole address the browser landed on", ErrProviderCredential)
		}
		c.forget(id)
		if o.Keyed != nil {
			kind = v1.CredentialKind_CREDENTIAL_KIND_KEY
			if key, err = o.Keyed(ctx, c.client, code, p.verifier); err != nil {
				return nil, err
			}
		} else {
			clientID = cmp.Or(issued, o.Config.ClientID)
			params := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {o.Config.RedirectURL}, "code_verifier": {p.verifier}}
			for k, v := range o.Token {
				params.Set(k, strings.ReplaceAll(v, "{state}", p.state))
			}
			if tok, err = c.token(ctx, o, clientID, params); err != nil {
				return nil, err
			}
		}
	}
	c.forget(id)
	if tok != nil && o.Requires != "" {
		if granted, _ := tok.Extra("scope").(string); granted != "" && !slices.Contains(strings.Fields(granted), o.Requires) {
			return nil, fmt.Errorf("%w: the sign-in left out %s, allow it when approving", ErrProviderCredential, o.Requires)
		}
	}
	row, err := c.newRow(ctx, owner, p.sourceID, p.name, kind)
	if err != nil {
		return nil, err
	}
	row.Secret = key
	if tok != nil {
		if err := c.keep(&row, tok, "", clientID); err != nil {
			return nil, err
		}
	}
	if err := c.save(ctx, row); err != nil {
		return nil, err
	}
	c.log.Info("provider sign-in stored", "owner", owner, "source", p.sourceID, "id", row.Credential.GetId())
	return c.reread(ctx, owner, row.Credential.GetId())
}

func (c *Credentials) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// The code, state and issued client of the address the browser landed on, or of a code pasted alone or as code#state
func landing(callback string) (code, state, client string, err error) {
	callback = strings.TrimSpace(callback)
	if u, perr := url.Parse(callback); perr == nil && u.Scheme != "" && u.RawQuery != "" {
		q := u.Query()
		if e := q.Get("error"); e != "" {
			return "", "", "", fmt.Errorf("%w: the provider answered %s", ErrProviderCredential, e)
		}
		code, state, client = q.Get("code"), q.Get("state"), q.Get("client_id")
	} else {
		code, state, _ = strings.Cut(callback, "#")
	}
	if code == "" {
		return "", "", "", fmt.Errorf("%w: paste the address the browser landed on after approving", ErrProviderCredential)
	}
	return code, state, client, nil
}

// Sends a token request, a form or JSON as the provider takes it, answering the tokens with the id token and granted scope among their extras
func (c *Credentials) token(ctx context.Context, o *sources.OAuth, clientID string, params url.Values) (*oauth2.Token, error) {
	if clientID != "" {
		params.Set("client_id", clientID)
	}
	body, contentType := []byte(params.Encode()), "application/x-www-form-urlencoded"
	if o.JSON {
		flat := map[string]string{}
		for k := range params {
			flat[k] = params.Get(k)
		}
		body, _ = json.Marshal(flat)
		contentType = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Config.Endpoint.TokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var answer struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
		Error        string `json:"error"`
	}
	json.Unmarshal(data, &answer)
	// The provider's answer may echo credential material, so only its status and code are kept
	if resp.StatusCode != http.StatusOK || answer.AccessToken == "" {
		return nil, fmt.Errorf("%w: the provider refused the tokens (%s %s)", ErrProviderCredential, resp.Status, answer.Error)
	}
	tok := &oauth2.Token{AccessToken: answer.AccessToken, RefreshToken: answer.RefreshToken, TokenType: answer.TokenType}
	if answer.ExpiresIn > 0 {
		tok.Expiry = c.now().Add(time.Duration(answer.ExpiresIn) * time.Second)
	}
	return tok.WithExtra(map[string]any{"id_token": answer.IDToken, "scope": answer.Scope}), nil
}

// Writes a token response into the row, a refresh token, id token or client it lacks kept from before
func (c *Credentials) keep(row *db.CredentialRow, tok *oauth2.Token, previous, clientID string) error {
	var old signedIn
	if previous != "" {
		json.Unmarshal([]byte(previous), &old)
	}
	idToken, _ := tok.Extra("id_token").(string)
	s := signedIn{AccessToken: tok.AccessToken, RefreshToken: cmp.Or(tok.RefreshToken, old.RefreshToken), IDToken: cmp.Or(idToken, old.IDToken), Expiry: tok.Expiry, ClientID: cmp.Or(clientID, old.ClientID)}
	if s.Expiry.IsZero() {
		s.Expiry = jwtExpiry(s.AccessToken)
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	row.Secret = string(data)
	row.Credential.ExpiresAt = nil
	if !s.Expiry.IsZero() {
		row.Credential.ExpiresAt = timestamppb.New(s.Expiry)
	}
	return nil
}

// The expiry a JWT access token states, zero when it is no JWT or states none
func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

// The credential a request to the source uses among the owner's, the first by position. Budgets will choose here later.
func chosen(rows []db.CredentialRow) (db.CredentialRow, bool) {
	if len(rows) == 0 {
		return db.CredentialRow{}, false
	}
	return rows[0], true
}

// The headers the owner's requests to a provider source carry beside the ones sent. ok is false for a source
// serving no models, and sources.ErrNoCredential says the owner keeps none for it.
func (c *Credentials) Headers(ctx context.Context, owner, sourceID string, sent http.Header) (map[string]string, bool, error) {
	src, cat, err := c.provider(sourceID)
	if err != nil || cat == nil {
		return nil, false, nil
	}
	rows, err := c.db.SourceCredentials(ctx, owner, sourceID)
	if err != nil {
		return nil, true, err
	}
	row, found := chosen(rows)
	if !found {
		return nil, true, fmt.Errorf("%w for %s, add one under Profile, Providers", sources.ErrNoCredential, sourceName(src, cat))
	}
	if row.Credential.GetKind() != v1.CredentialKind_CREDENTIAL_KIND_SIGN_IN || cat.SignIn.OAuth == nil {
		return cat.SignIn.Headers(row.Secret, sent), true, nil
	}
	o := cat.SignIn.OAuth
	tok, err := c.fresh(ctx, o, row)
	if err != nil {
		return nil, true, fmt.Errorf("%s: %w", sourceName(src, cat), err)
	}
	out := cat.SignIn.Headers(tok.AccessToken, sent)
	for k, v := range o.Fixed {
		if sent.Get(k) == "" {
			out[k] = v
		}
	}
	if o.Claims != nil {
		for k, v := range o.Claims(tok) {
			out[k] = v
		}
	}
	return out, true, nil
}

// The caller's headers for each provider source, for the sources a request reaches
func (c *Credentials) Authorizer(ctx context.Context, owner string) func(sourceID string) http.Header {
	return func(sourceID string) http.Header {
		h, _, err := c.Headers(ctx, owner, sourceID, nil)
		if err != nil || h == nil {
			return nil
		}
		out := http.Header{}
		for k, v := range h {
			out.Set(k, v)
		}
		return out
	}
}

// A sign-in's tokens, refreshed and stored first when they lapse within the margin
func (c *Credentials) fresh(ctx context.Context, o *sources.OAuth, row db.CredentialRow) (*oauth2.Token, error) {
	var s signedIn
	if err := json.Unmarshal([]byte(row.Secret), &s); err != nil {
		return nil, fmt.Errorf("the stored sign-in is unreadable, sign in again")
	}
	if c.now().Add(refreshMargin).Before(s.Expiry) {
		return s.token(), nil
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	// Another request may have refreshed while this one waited
	if again, err := c.db.GetCredential(ctx, row.Owner, row.Credential.GetId()); err == nil {
		row = again
		if json.Unmarshal([]byte(row.Secret), &s) == nil && c.now().Add(refreshMargin).Before(s.Expiry) {
			return s.token(), nil
		}
	}
	if s.RefreshToken == "" {
		return nil, fmt.Errorf("the sign-in lapsed and holds no refresh token, sign in again")
	}
	params := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.RefreshToken}}
	for k, v := range o.Token {
		if !strings.Contains(v, "{") {
			params.Set(k, v)
		}
	}
	next, err := c.token(ctx, o, cmp.Or(s.ClientID, o.Config.ClientID), params)
	if err != nil && c.now().Before(s.Expiry) {
		c.log.Warn("sign-in not refreshed, its access holds a while yet", "owner", row.Owner, "id", row.Credential.GetId())
		return s.token(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("the sign-in could not be refreshed, sign in again: %w", err)
	}
	if err := c.keep(&row, next, row.Secret, ""); err != nil {
		return nil, err
	}
	row.Credential.UpdatedAt = timestamppb.New(c.now().UTC())
	if err := c.save(ctx, row); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(row.Secret), &s)
	return s.token(), nil
}

// The stored tokens as the oauth2 package holds them, the id token among their extras
func (s signedIn) token() *oauth2.Token {
	tok := &oauth2.Token{AccessToken: s.AccessToken, RefreshToken: s.RefreshToken, TokenType: "Bearer", Expiry: s.Expiry}
	return tok.WithExtra(map[string]any{"id_token": s.IDToken})
}
