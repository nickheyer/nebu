package auth

import (
	"bytes"
	"cmp"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math/big"
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
	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxCredentialName = 64
	// Credentials one owner may keep
	maxCredentials = 100
	// How long before a sign-in's access lapses its tokens are refreshed
	refreshMargin = 5 * time.Minute
	// How long a refresh may take once begun, the request that began it no longer bounding it
	refreshFor = 30 * time.Second
	// How long after a refresh failed the next is left untried while the access it would replace still holds
	refreshRetry = 30 * time.Second
	// How long a sign-in by redirect waits for the address the person copies back
	redirectTTL = 10 * time.Minute
	// How long removing a sign-in waits on its provider to revoke its session before answering, a session not revoked by then
	// asked about again apart from any request: how often the queue is looked at, the wait after a first failure doubling to the
	// most, how long one try may take, and how long a session is asked about before it is given up on
	revokeWait    = 3 * time.Second
	revokeEvery   = time.Minute
	revokeFirst   = time.Minute
	revokeMost    = time.Hour
	revokeTimeout = 10 * time.Second
	revokeKeep    = 30 * 24 * time.Hour
	// How long a provider's signing keys stand once read, how often reading them is tried, and the wait after the first failure
	keysFor   = time.Hour
	keysTries = 3
	keysWait  = 300 * time.Millisecond
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
	// What each credential's provider reports of its limits and its cooldowns, by credential id
	standings map[string]*answers
	// One refresh of a sign-in at a time, since a provider may take each refresh token once, every request wanting its tokens
	// meanwhile waiting on that one, and what holds a sign-in's tokens while they are replaced or removed, both by credential id
	refreshes singleflight.Group
	holds     sync.Map
	// When a sign-in whose refresh failed is next refreshed, by credential id, its access holding until then
	retries sync.Map
	// The signing keys providers publish, by the address they are read from
	keys map[string]*keySet
	// Usage recorded since the last write, held whole while a write runs
	ledger    sync.RWMutex
	unwritten map[usageKey]*v1.CredentialUsage
}

// A sign-in the person has yet to approve, by device or by the address the provider sends the browser to
type pendingSignIn struct {
	owner, sourceID, name string
	expires               time.Time
	device                *sources.Device
	state, verifier       string
	// The nonce the authorization request sent, which the ID token must carry back
	nonce string
	// The sign-in authorized again, its tokens replaced in place, and the client its provider issued it, empty for a new credential
	again, client string
}

// A provider's signing keys by id with when they were read
type keySet struct {
	keys map[string]*rsa.PublicKey
	read time.Time
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
	return &Credentials{db: store, sources: srcs, log: log, client: &http.Client{Timeout: time.Minute}, now: time.Now, host: host, pending: map[string]*pendingSignIn{}, standings: map[string]*answers{}, keys: map[string]*keySet{}, unwritten: map[usageKey]*v1.CredentialUsage{}}
}

// The owner a caller's provider credentials are kept under, the shared one for the daemon token and auth.disabled, which name no account,
// a local account's id as generated, and any other subject under its provider, so no account reaches the shared owner or another provider's
func CredentialOwner(id Identity) string {
	switch {
	case id.Subject == "":
		return ProviderDaemon
	case id.Provider == ProviderLocal && id.Subject != ProviderDaemon && !strings.Contains(id.Subject, ":"):
		return id.Subject
	}
	return id.Provider + ":" + id.Subject
}

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

// How the source's provider takes OpenAI's stream_options: whether a stream names its usage only when asked through it, and
// whether a body naming it at all is refused, neither for a source serving no models. The server is the endpoint the source
// was given, so what is learned of one server's answers is not held against another the source is pointed at later.
func (c *Credentials) StreamOptions(sourceID string) (asks, refuses bool, server string) {
	src, cat, err := c.provider(sourceID)
	if err != nil || cat == nil {
		return false, false, ""
	}
	return cat.StreamUsage, cat.NoStreamOptions, src.Spec().GetConfig()["endpoint"]
}

// The name a source goes by in messages
func sourceName(src sources.Source, cat *sources.Catalog) string {
	if name := src.Spec().GetName(); name != "" {
		return name
	}
	return cat.Name
}

// The owner's credentials by source and position with their windows as they stand, never with a secret
func (c *Credentials) List(ctx context.Context, owner string) ([]*v1.ProviderCredential, error) {
	rows, err := c.db.ListCredentials(ctx, owner)
	if err != nil {
		return nil, err
	}
	now := c.now()
	out := make([]*v1.ProviderCredential, 0, len(rows))
	for _, r := range rows {
		for _, w := range c.windows(ctx, r, now, "") {
			r.Credential.Windows = append(r.Credential.Windows, w.CredentialWindow)
		}
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
	if in.GetBudgetsSet() {
		if err := checkBudgets(in.GetBudgets()); err != nil {
			return nil, err
		}
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
		if in.GetBudgetsSet() {
			row.Credential.Budgets = in.GetBudgets()
		}
		if err := c.db.PutCredential(ctx, row); err != nil {
			return nil, err
		}
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
		// Only what the request names is written, so a sign-in refreshed meanwhile keeps its new tokens
		written, err := c.db.EditCredential(ctx, owner, in.GetId(), name, secret, in.GetBudgets(), in.GetBudgetsSet(), c.now().UTC())
		if err := wrote(in.GetId(), written, err); err != nil {
			return nil, err
		}
		// What the provider answered under the key replaced says nothing of the new one
		if secret != "" {
			c.forgetStanding(in.GetId())
		}
	}
	if in.GetPosition() > 0 {
		if err := c.place(ctx, owner, row.Credential.GetSourceId(), row.Credential.GetId(), in.GetPosition()); err != nil {
			return nil, err
		}
	}
	c.log.Info("provider credential stored", "owner", owner, "source", row.Credential.GetSourceId(), "id", row.Credential.GetId())
	return c.reread(ctx, owner, row.Credential.GetId())
}

// Moves one of the owner's credentials for a source to a position from 1, the others keeping their order around it
func (c *Credentials) place(ctx context.Context, owner, sourceID, id string, position uint32) error {
	rows, err := c.db.SourceCredentials(ctx, owner, sourceID)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(rows, func(r db.CredentialRow) bool { return r.Credential.GetId() == id })
	if i < 0 {
		return fmt.Errorf("%w: %s", ErrUnknownCredential, id)
	}
	moved := rows[i]
	rows = slices.Delete(rows, i, i+1)
	rows = slices.Insert(rows, min(int(position)-1, len(rows)), moved)
	for n, r := range rows {
		if r.Credential.GetPosition() == uint32(n+1) {
			continue
		}
		// One removed meanwhile leaves the rest to take their places
		if _, err := c.db.SetCredentialPosition(ctx, owner, r.Credential.GetId(), uint32(n+1)); err != nil {
			return err
		}
	}
	return nil
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

// The error of a write to one of an owner's credentials, unknown when the owner no longer holds it
func wrote(id string, written bool, err error) error {
	if err == nil && !written {
		err = fmt.Errorf("%w: %s", ErrUnknownCredential, id)
	}
	return err
}

// Holds a sign-in's tokens for one writer, a refresh, a second authorization or a removal, answering what lets go of them
func (c *Credentials) hold(id string) func() {
	mu, _ := c.holds.LoadOrStore(id, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	return mu.(*sync.Mutex).Unlock
}

// Removes one of the owner's credentials. A sign-in's session is revoked at a provider that revokes them: its refresh token
// is queued for that in the write removing it, asked about once while the caller waits a moment, and again apart from any
// request until the provider confirms, answering whether it has yet to
func (c *Credentials) Delete(ctx context.Context, owner, id string) (bool, error) {
	row, err := c.get(ctx, owner, id)
	if err != nil {
		return false, err
	}
	release := func() {}
	if row.Credential.GetKind() == v1.CredentialKind_CREDENTIAL_KIND_SIGN_IN {
		// Read again and removed while its tokens are held, so no refresh leaves a newer refresh token alive than the one revoked
		release = c.hold(id)
		if again, err := c.db.GetCredential(ctx, owner, id); err == nil {
			row = again
		}
	}
	rev := c.revocation(row)
	ok, err := c.db.DeleteCredential(ctx, owner, id, rev)
	release()
	if err := wrote(id, ok, err); err != nil {
		return false, err
	}
	c.holds.Delete(id)
	c.retries.Delete(id)
	c.forgetStanding(id)
	c.log.Info("provider credential removed", "owner", owner, "id", id)
	if rev == nil {
		return false, nil
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeWait)
	defer cancel()
	if err := c.revoke(rctx, *rev); err != nil {
		c.log.Warn("the provider has yet to confirm revoking the removed sign-in, asked again until it does", "owner", owner, "id", id, "err", err)
		return true, nil
	}
	return false, nil
}

// What revoking a credential's session takes, nil for a key, a sign-in holding no refresh token, or a provider revoking none
func (c *Credentials) revocation(row db.CredentialRow) *db.Revocation {
	if row.Credential.GetKind() != v1.CredentialKind_CREDENTIAL_KIND_SIGN_IN {
		return nil
	}
	_, cat, err := c.provider(row.Credential.GetSourceId())
	if err != nil || cat == nil || cat.SignIn.OAuth == nil {
		return nil
	}
	var s signedIn
	if json.Unmarshal([]byte(row.Secret), &s) != nil {
		return nil
	}
	return c.revocationOf(cat.SignIn.OAuth, row.Credential.GetId(), s.RefreshToken, s.ClientID)
}

// The revocation of a refresh token under the client it was issued to, nil without a token or at a provider revoking none
func (c *Credentials) revocationOf(o *sources.OAuth, id, refresh, clientID string) *db.Revocation {
	if o.Revoke == "" || refresh == "" {
		return nil
	}
	return &db.Revocation{ID: id, Endpoint: o.Revoke, ClientID: cmp.Or(clientID, o.Config.ClientID), Token: refresh, Since: c.now().UTC()}
}

// Queues the session of tokens a sign-in was issued and then refused for revocation, so none outlives the refusal unseen
func (c *Credentials) unwanted(ctx context.Context, o *sources.OAuth, tok *oauth2.Token, clientID string) {
	rev := c.revocationOf(o, db.NewID(), tok.RefreshToken, clientID)
	if rev == nil {
		return
	}
	if err := c.db.QueueRevocation(context.WithoutCancel(ctx), *rev); err != nil {
		c.log.Warn("a refused sign-in's session not queued for revocation", "err", err)
	}
}

// Asks the provider once to revoke a queued token, forgetting it once revoked. One the provider refuses outright is forgotten
// as well, asking again only repeating the refusal, and one it failed to take for now is kept for the next try.
func (c *Credentials) revoke(ctx context.Context, r db.Revocation) error {
	form := url.Values{"token": {r.Token}, "token_type_hint": {"refresh_token"}, "client_id": {r.ClientID}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Endpoint, strings.NewReader(form))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// What becomes of the token is written whatever became of the request waiting on it
	kept := context.WithoutCancel(ctx)
	resp, err := c.client.Do(req)
	if err == nil {
		resp.Body.Close()
		switch status := resp.StatusCode; {
		case status == http.StatusOK:
			return c.db.DropRevocation(kept, r.ID)
		case status >= http.StatusInternalServerError || status == http.StatusTooManyRequests || status == http.StatusRequestTimeout:
			err = fmt.Errorf("the provider answered %s", resp.Status)
		default:
			c.db.DropRevocation(kept, r.ID)
			return fmt.Errorf("the provider refused the revocation with %s", resp.Status)
		}
	}
	if terr := c.db.TriedRevocation(kept, r.ID, c.now().UTC()); terr != nil {
		c.log.Warn("a try at a revocation not noted", "id", r.ID, "err", terr)
	}
	return err
}

// Asks providers again to revoke the sessions still queued, each once the wait since its last try has passed, one queued
// longer than a session is asked about given up on
func (c *Credentials) revokeQueued(ctx context.Context) {
	rows, err := c.db.Revocations(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Warn("queued revocations not read", "err", err)
		}
		return
	}
	now := c.now()
	for _, r := range rows {
		if ctx.Err() != nil {
			return
		}
		if now.Sub(r.Since) > revokeKeep {
			c.db.DropRevocation(ctx, r.ID)
			c.log.Warn("a removed sign-in's session was never confirmed revoked and is given up on, disconnect nebu at the provider to end it", "id", r.ID, "tries", r.Tries)
			continue
		}
		if r.Tries > 0 && now.Before(r.Tried.Add(min(revokeFirst<<min(r.Tries-1, 16), revokeMost))) {
			continue
		}
		tctx, cancel := context.WithTimeout(ctx, revokeTimeout)
		err := c.revoke(tctx, r)
		cancel()
		if err != nil {
			c.log.Debug("a removed sign-in's session not revoked yet", "id", r.ID, "tries", r.Tries+1, "err", err)
			continue
		}
		c.log.Info("a removed sign-in's session was revoked at its provider", "id", r.ID)
	}
}

// Random characters for a state or a nonce
func random() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Starts a sign-in at the provider: a code the person enters, or an address the person approves at and copies the landing address back from.
// Naming one of the owner's sign-ins authorizes it again, under the client its provider issued it, its tokens then replaced in place.
func (c *Credentials) StartSignIn(ctx context.Context, owner, sourceID, name, again string) (*v1.ProviderSignIn, error) {
	src, cat, err := c.provider(sourceID)
	if err != nil {
		return nil, err
	}
	if cat == nil || cat.SignIn.OAuth == nil {
		return nil, fmt.Errorf("%w: source %s takes no sign-in", ErrProviderCredential, sourceID)
	}
	o := cat.SignIn.OAuth
	p := &pendingSignIn{owner: owner, sourceID: sourceID, name: cmp.Or(strings.TrimSpace(name), sourceName(src, cat)), again: again}
	if again != "" {
		row, err := c.get(ctx, owner, again)
		switch {
		case err != nil:
			return nil, err
		case row.Credential.GetKind() != v1.CredentialKind_CREDENTIAL_KIND_SIGN_IN:
			return nil, fmt.Errorf("%w: %s is a key, not a sign-in", ErrProviderCredential, again)
		case row.Credential.GetSourceId() != sourceID:
			return nil, fmt.Errorf("%w: %s is a sign-in with another source", ErrProviderCredential, again)
		}
		var kept signedIn
		json.Unmarshal([]byte(row.Secret), &kept)
		if o.Issues {
			p.client = kept.ClientID
		}
	}
	out := &v1.ProviderSignIn{SourceId: sourceID}
	if o.Device != nil {
		if p.device, err = o.Device(ctx, c.client); err != nil {
			return nil, fmt.Errorf("sign-in with %s: %w", sourceName(src, cat), err)
		}
		p.expires, out.Url, out.Code = p.device.Expires, p.device.URL, p.device.Code
	} else {
		p.state, p.verifier, p.nonce, p.expires = random(), oauth2.GenerateVerifier(), random(), c.now().Add(redirectTTL)
		opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(p.verifier)}
		config, params := o.Config, maps.Clone(o.Authorize)
		if p.client == "" {
			maps.Copy(params, o.Registers)
		} else {
			// Authorized again under the client it was issued. The ID token kept from before could name the account as a hint,
			// but the address would carry it into the browser's history, so the person picks the account and the one picked is
			// checked against the one signed in before its tokens are replaced
			config.ClientID = p.client
		}
		for k, v := range params {
			opts = append(opts, oauth2.SetAuthURLParam(k, strings.NewReplacer("{host}", c.host, "{nonce}", p.nonce).Replace(v)))
		}
		out.Url = config.AuthCodeURL(p.state, opts...)
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
		case state == "" && o.Config.RedirectURL != "":
			return nil, fmt.Errorf("%w: the address carries no state, copy the whole address the browser landed on", ErrProviderCredential)
		case state != "" && state != p.state:
			return nil, fmt.Errorf("%w: the address belongs to another sign-in", ErrProviderCredential)
		case o.Issues && issued == "" && p.client == "":
			return nil, fmt.Errorf("%w: the address names no client, copy the whole address the browser landed on", ErrProviderCredential)
		case p.client != "" && issued != "" && issued != p.client:
			return nil, fmt.Errorf("%w: the address names another client than the one this sign-in was issued, start the sign-in again", ErrProviderCredential)
		}
		// The provider's signing keys are read before the code is spent, so keys out of reach leave the sign-in to be finished once they are back
		if o.Keyed == nil && o.JWKS != "" {
			if _, err := c.signingKeys(ctx, o.JWKS, false); err != nil {
				return nil, err
			}
		}
		c.forget(id)
		if o.Keyed != nil {
			kind = v1.CredentialKind_CREDENTIAL_KIND_KEY
			if key, err = o.Keyed(ctx, c.client, code, p.verifier); err != nil {
				return nil, err
			}
		} else {
			clientID = cmp.Or(p.client, issued, o.Config.ClientID)
			params := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {o.Config.RedirectURL}, "code_verifier": {p.verifier}}
			for k, v := range o.Token {
				params.Set(k, strings.ReplaceAll(v, "{state}", p.state))
			}
			if tok, err = c.token(ctx, o, clientID, params); err != nil {
				return nil, err
			}
			if o.JWKS != "" {
				if err := c.verifyID(ctx, o, tok, clientID, p.nonce); err != nil {
					c.unwanted(ctx, o, tok, clientID)
					return nil, err
				}
			}
		}
	}
	c.forget(id)
	if tok != nil && o.Requires != "" {
		if granted, _ := tok.Extra("scope").(string); granted != "" && !slices.Contains(strings.Fields(granted), o.Requires) {
			c.unwanted(ctx, o, tok, clientID)
			return nil, fmt.Errorf("%w: the sign-in left out %s, allow it when approving", ErrProviderCredential, o.Requires)
		}
	}
	if p.again != "" && tok != nil {
		return c.signedAgain(ctx, o, owner, p.again, tok, clientID)
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
	if err := c.db.PutCredential(ctx, row); err != nil {
		return nil, err
	}
	c.log.Info("provider sign-in stored", "owner", owner, "source", p.sourceID, "id", row.Credential.GetId())
	return c.reread(ctx, owner, row.Credential.GetId())
}

// Replaces the tokens of a sign-in authorized again, whatever the provider's answers under the old ones left forgotten. The
// account approved must be the one signed in before, another's tokens refused and their session queued for revocation.
func (c *Credentials) signedAgain(ctx context.Context, o *sources.OAuth, owner, id string, tok *oauth2.Token, clientID string) (*v1.ProviderCredential, error) {
	// Its tokens held, so a refresh of the old ones under way does not write over the new ones
	defer c.hold(id)()
	row, err := c.get(ctx, owner, id)
	if err != nil {
		c.unwanted(ctx, o, tok, clientID)
		return nil, err
	}
	var old signedIn
	json.Unmarshal([]byte(row.Secret), &old)
	idToken, _ := tok.Extra("id_token").(string)
	if was, is := subjectOf(old.IDToken), subjectOf(idToken); was != "" && is != "" && was != is {
		c.unwanted(ctx, o, tok, clientID)
		return nil, fmt.Errorf("%w: that is another account than the one %s is signed in to, approve with the same account or add a new sign-in", ErrProviderCredential, row.Credential.GetName())
	}
	if err := c.keep(&row, tok, row.Secret, clientID); err != nil {
		return nil, err
	}
	written, err := c.db.SetCredentialSecret(ctx, owner, id, row.Secret, row.Credential.GetExpiresAt(), c.now().UTC())
	if err := wrote(id, written, err); err != nil {
		return nil, err
	}
	c.forgetStanding(id)
	c.log.Info("provider sign-in authorized again", "owner", owner, "source", row.Credential.GetSourceId(), "id", id)
	return c.reread(ctx, owner, id)
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

// The subject an ID token names, read without checking the token, empty when it names none
func subjectOf(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	json.Unmarshal(payload, &claims)
	return claims.Sub
}

// Checks the ID token a code exchange answered: signed by a key the provider publishes, from its issuer, for the
// client, unexpired, and carrying the nonce the sign-in sent
func (c *Credentials) verifyID(ctx context.Context, o *sources.OAuth, tok *oauth2.Token, clientID, nonce string) error {
	idToken, _ := tok.Extra("id_token").(string)
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return fmt.Errorf("%w: the provider answered no ID token", ErrProviderCredential)
	}
	decode := func(part string, v any) error {
		data, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, v)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	var claims struct {
		Iss   string          `json:"iss"`
		Aud   json.RawMessage `json:"aud"`
		Exp   int64           `json:"exp"`
		Nonce string          `json:"nonce"`
	}
	if decode(parts[0], &header) != nil || decode(parts[1], &claims) != nil {
		return fmt.Errorf("%w: the provider's ID token is unreadable", ErrProviderCredential)
	}
	// RS256, the algorithm every OpenID provider signs with, is the one checked
	if header.Alg != "RS256" {
		return fmt.Errorf("%w: the provider's ID token is signed with %q, not RS256", ErrProviderCredential, header.Alg)
	}
	key, err := c.signingKey(ctx, o.JWKS, header.Kid)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if sig, err := base64.RawURLEncoding.DecodeString(parts[2]); err != nil || rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return fmt.Errorf("%w: the provider's ID token is not signed by its key", ErrProviderCredential)
	}
	// An audience is one client or a list of them
	var aud []string
	if json.Unmarshal(claims.Aud, &aud) != nil {
		aud = []string{""}
		json.Unmarshal(claims.Aud, &aud[0])
	}
	switch {
	case claims.Iss != o.Issuer:
		return fmt.Errorf("%w: the ID token is from %q, not %q", ErrProviderCredential, claims.Iss, o.Issuer)
	case !slices.Contains(aud, clientID):
		return fmt.Errorf("%w: the ID token is for another client than the one issued", ErrProviderCredential)
	case !c.now().Before(time.Unix(claims.Exp, 0)):
		return fmt.Errorf("%w: the ID token has expired, sign in again", ErrProviderCredential)
	case claims.Nonce != nonce:
		return fmt.Errorf("%w: the ID token belongs to another sign-in", ErrProviderCredential)
	}
	return nil
}

// The RSA key the provider publishes under an id, its keys read anew when the id is not among those kept, as once it changed them
func (c *Credentials) signingKey(ctx context.Context, jwks, kid string) (*rsa.PublicKey, error) {
	for _, anew := range []bool{false, true} {
		keys, err := c.signingKeys(ctx, jwks, anew)
		if err != nil {
			return nil, err
		}
		if key := keys[kid]; key != nil {
			return key, nil
		}
	}
	return nil, fmt.Errorf("%w: the provider publishes no key %q its ID token names", ErrProviderCredential, kid)
}

// The RSA keys a provider publishes at its JWKS address by id, standing a while once read. Reading them is tried a few times,
// and when that fails the keys read before stand on, a provider changing its keys seldom.
func (c *Credentials) signingKeys(ctx context.Context, jwks string, anew bool) (map[string]*rsa.PublicKey, error) {
	c.mu.Lock()
	kept := c.keys[jwks]
	c.mu.Unlock()
	if kept != nil && !anew && c.now().Before(kept.read.Add(keysFor)) {
		return kept.keys, nil
	}
	var err error
	wait := keysWait
tries:
	for try := 1; try <= keysTries; try++ {
		if try > 1 {
			select {
			case <-ctx.Done():
				break tries
			case <-time.After(wait):
				wait *= 3
			}
		}
		var keys map[string]*rsa.PublicKey
		if keys, err = c.readKeys(ctx, jwks); err == nil {
			c.mu.Lock()
			c.keys[jwks] = &keySet{keys: keys, read: c.now()}
			c.mu.Unlock()
			return keys, nil
		}
	}
	if kept != nil {
		return kept.keys, nil
	}
	return nil, fmt.Errorf("%w: the provider's signing keys could not be read, finish the sign-in again in a moment: %v", ErrProviderCredential, err)
}

// Reads the RSA keys published at a JWKS address
func (c *Credentials) readKeys(ctx context.Context, jwks string) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwks, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", jwks, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("%s is unreadable: %v", jwks, err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		n, nerr := base64.RawURLEncoding.DecodeString(k.N)
		e, eerr := base64.RawURLEncoding.DecodeString(k.E)
		if k.Kty == "RSA" && nerr == nil && eerr == nil && len(n) > 0 && len(e) > 0 && len(e) <= 4 {
			keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%s publishes no RSA key", jwks)
	}
	return keys, nil
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

// The credential the owner's requests for a model of a provider source go under and the headers they carry beside
// the ones sent, an empty model counting for every model. Both are nil for a source serving no models,
// sources.ErrNoCredential says the owner keeps none for it, and a requestcontrol.SpentError that every one is spent.
func (c *Credentials) Headers(ctx context.Context, owner, sourceID, model string, sent http.Header) (*v1.ProviderCredential, map[string]string, error) {
	src, cat, err := c.provider(sourceID)
	if err != nil || cat == nil {
		return nil, nil, nil
	}
	rows, err := c.db.SourceCredentials(ctx, owner, sourceID)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("%w for %s, add one under Profile, Providers", sources.ErrNoCredential, sourceName(src, cat))
	}
	row, reset, found := c.chosen(ctx, rows, model)
	if !found {
		now := c.now()
		return nil, nil, spent(sourceName(src, cat), model, reset, now, c.rejected(rows, now), len(rows))
	}
	out, err := c.sign(ctx, src, cat, row, sent)
	if err != nil {
		return nil, nil, err
	}
	c.retrying(row.Credential.GetId(), c.now())
	return row.Credential, out, nil
}

// The headers a request under the credential carries beside the ones sent, a sign-in's tokens refreshed first
func (c *Credentials) sign(ctx context.Context, src sources.Source, cat *sources.Catalog, row db.CredentialRow, sent http.Header) (map[string]string, error) {
	if row.Credential.GetKind() != v1.CredentialKind_CREDENTIAL_KIND_SIGN_IN || cat.SignIn.OAuth == nil {
		return cat.SignIn.Headers(row.Secret, sent), nil
	}
	o := cat.SignIn.OAuth
	tok, err := c.fresh(ctx, o, row)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", sourceName(src, cat), err)
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
	return out, nil
}

// The caller's headers for each provider source, for the sources a request reaches, the first credential nothing holds
// back signing or else the first by position, since listing models spends nothing a budget counts
func (c *Credentials) Authorizer(ctx context.Context, owner string) func(sourceID string) http.Header {
	return func(sourceID string) http.Header {
		src, cat, err := c.provider(sourceID)
		if err != nil || cat == nil {
			return nil
		}
		rows, err := c.db.SourceCredentials(ctx, owner, sourceID)
		if err != nil || len(rows) == 0 {
			return nil
		}
		row, _, free := c.chosen(ctx, rows, "")
		if !free {
			row = rows[0]
		}
		h, err := c.sign(ctx, src, cat, row, nil)
		if err != nil {
			return nil
		}
		out := http.Header{}
		for k, v := range h {
			out.Set(k, v)
		}
		return out
	}
}

// The header a member names the owner in whose provider credentials a request it sends on goes under
const CredentialOwnerHeader = "X-Nebu-Credential-Owner"

type signedKey struct{}

// Signs what a context reaches of provider sources with an owner's credentials, the owner kept beside them for the work the context starts
func (c *Credentials) Sign(ctx context.Context, owner string) context.Context {
	return c.signAs(ctx, func() string { return owner })
}

// Signs a context with the credentials of whoever sent the headers, found out once a provider source is reached
func (c *Credentials) SignCaller(ctx context.Context, g *Guard, h http.Header) context.Context {
	return c.signAs(ctx, sync.OnceValue(func() string { return g.CredentialOwner(h) }))
}

// Without credentials at hand the owner alone is kept, for a member the work is sent on to
func (c *Credentials) signAs(ctx context.Context, owner func() string) context.Context {
	ctx = context.WithValue(ctx, signedKey{}, owner)
	if c == nil {
		return ctx
	}
	return sources.WithAuthorizer(ctx, func(sourceID string) http.Header { return c.Authorizer(ctx, owner())(sourceID) })
}

// The owner a context is signed for, the shared one for a context never signed, as the daemon's own work is
func SignedFor(ctx context.Context) string {
	if owner, ok := ctx.Value(signedKey{}).(func() string); ok {
		return owner()
	}
	return ProviderDaemon
}

// A sign-in's tokens, refreshed and stored first when they lapse within the margin. A refresh once begun runs to its end
// whatever becomes of the request behind it: the provider replaces the refresh token as it answers, and one cut off before
// that answer is stored would leave a token already spent. So it runs apart from the request, one at a time for each sign-in
// and shared by every request wanting its tokens meanwhile, each waiting no longer than its own time and none on another sign-in's.
func (c *Credentials) fresh(ctx context.Context, o *sources.OAuth, row db.CredentialRow) (*oauth2.Token, error) {
	var s signedIn
	if err := json.Unmarshal([]byte(row.Secret), &s); err != nil {
		return nil, fmt.Errorf("the stored sign-in is unreadable, sign in again")
	}
	if c.now().Add(refreshMargin).Before(s.Expiry) {
		return s.token(), nil
	}
	// A provider that just failed a refresh is not asked again by every request while the access still holds
	if at, failed := c.retries.Load(row.Credential.GetId()); failed && c.now().Before(at.(time.Time)) && c.now().Before(s.Expiry) {
		return s.token(), nil
	}
	done := c.refreshes.DoChan(row.Credential.GetId(), func() (any, error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshFor)
		defer cancel()
		return c.refresh(rctx, o, row)
	})
	select {
	case r := <-done:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.(signedIn).token(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Refreshes a sign-in's tokens and stores them, its tokens held meanwhile against a removal or a second authorization
func (c *Credentials) refresh(ctx context.Context, o *sources.OAuth, row db.CredentialRow) (signedIn, error) {
	defer c.hold(row.Credential.GetId())()
	var s signedIn
	// Another request may have refreshed since this one read the row, or the credential may have been removed, its token then left unspent
	again, err := c.db.GetCredential(ctx, row.Owner, row.Credential.GetId())
	if db.IsNotFound(err) {
		return s, fmt.Errorf("%w: %s", ErrUnknownCredential, row.Credential.GetId())
	}
	if err == nil {
		row = again
	}
	if err := json.Unmarshal([]byte(row.Secret), &s); err != nil {
		return s, fmt.Errorf("the stored sign-in is unreadable, sign in again")
	}
	if c.now().Add(refreshMargin).Before(s.Expiry) {
		return s, nil
	}
	if s.RefreshToken == "" {
		return s, fmt.Errorf("the sign-in lapsed and holds no refresh token, sign in again")
	}
	params := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.RefreshToken}}
	for k, v := range o.Token {
		if !strings.Contains(v, "{") {
			params.Set(k, v)
		}
	}
	next, err := c.token(ctx, o, cmp.Or(s.ClientID, o.Config.ClientID), params)
	if err != nil && c.now().Before(s.Expiry) {
		c.log.Warn("sign-in not refreshed, its access holds a while yet", "owner", row.Owner, "id", row.Credential.GetId(), "err", err)
		c.retries.Store(row.Credential.GetId(), c.now().Add(refreshRetry))
		return s, nil
	}
	c.retries.Delete(row.Credential.GetId())
	if err != nil {
		return s, fmt.Errorf("the sign-in could not be refreshed, sign in again: %w", err)
	}
	if err := c.keep(&row, next, row.Secret, ""); err != nil {
		return s, err
	}
	// The tokens alone are written, whatever the owner changed of the credential meanwhile left standing
	written, err := c.db.SetCredentialSecret(ctx, row.Owner, row.Credential.GetId(), row.Secret, row.Credential.GetExpiresAt(), c.now().UTC())
	if err := wrote(row.Credential.GetId(), written, err); err != nil {
		return s, err
	}
	json.Unmarshal([]byte(row.Secret), &s)
	return s, nil
}

// The stored tokens as the oauth2 package holds them, the id token among their extras
func (s signedIn) token() *oauth2.Token {
	tok := &oauth2.Token{AccessToken: s.AccessToken, RefreshToken: s.RefreshToken, TokenType: "Bearer", Expiry: s.Expiry}
	return tok.WithExtra(map[string]any{"id_token": s.IDToken})
}
