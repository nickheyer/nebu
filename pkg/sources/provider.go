package sources

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Returned when the caller keeps no credential for a provider source
var ErrNoCredential = errors.New("no credential")

// How a caller signs in to a provider serving models: with a key, with a sign-in at the provider, or either
type SignIn struct {
	// The header a key or access token goes in, and the scheme before it
	Header, Scheme string
	// Headers every request to the provider carries unless the client sent its own
	Fixed map[string]string
	// Whether a key the caller gives is taken
	Key bool
	// The sign-in at the provider, nil for a provider taking a key alone
	OAuth *OAuth
}

// A sign-in at the provider, its tokens refreshed by the daemon
type OAuth struct {
	// The client, its endpoints, its redirect and the scopes asked for
	Config oauth2.Config
	// Parameters the authorization request carries beside the standard ones, {host} and {nonce} filled in
	Authorize map[string]string
	// Parameters the authorization request of a first sign-in carries beside those, a sign-in authorized again under the client it was issued leaving them out
	Registers map[string]string
	// Parameters every token request carries, such as a resource, {state} filled in on the code exchange
	Token map[string]string
	// Token requests go as JSON rather than a form
	JSON bool
	// The redirect names a client the provider issued this sign-in, used for its tokens from then on
	Issues bool
	// The scope the granted tokens must hold to serve requests
	Requires string
	// Where the provider publishes the keys a sign-in by redirect's ID token is checked against, signed RS256 by Issuer for
	// the client and carrying the {nonce} Authorize sent; empty for a provider whose ID token is not checked
	JWKS, Issuer string
	// Where a sign-in's refresh token is revoked when it is removed, empty for a provider taking no revocation
	Revoke string
	// Headers requests signed with the tokens carry beside the provider's fixed ones
	Fixed map[string]string
	// Asks for a code the person enters, for a sign-in by device, nil for one by redirect
	Device func(ctx context.Context, client *http.Client) (*Device, error)
	// Headers the tokens name, such as the workspace a provider takes in its own header
	Claims func(tok *oauth2.Token) map[string]string
	// Trades the redirect's code for a key, for a sign-in that ends in a key rather than tokens
	Keyed func(ctx context.Context, client *http.Client, code, verifier string) (string, error)
}

// A device sign-in waiting for the person
type Device struct {
	URL, Code string
	Expires   time.Time
	// Waits until the person approves, answering the tokens
	Wait func(ctx context.Context) (*oauth2.Token, error)
}

// The headers a provider reports its rate limits in, {window} in Header standing for each of Windows and {field} for limit, remaining and reset
type Limits struct {
	Header  string
	Windows []string
}

// One window's limit, what is left and when it resets as an answer's headers state them, false when they state no limit
func (l *Limits) Read(h http.Header, window string, now time.Time) (limit, remaining float64, reset time.Time, ok bool) {
	field := func(f string) string {
		return strings.TrimSpace(h.Get(strings.NewReplacer("{window}", window, "{field}", f).Replace(l.Header)))
	}
	limit, err := strconv.ParseFloat(field("limit"), 64)
	if err != nil || limit <= 0 {
		return 0, 0, time.Time{}, false
	}
	if remaining, err = strconv.ParseFloat(field("remaining"), 64); err != nil {
		return 0, 0, time.Time{}, false
	}
	return limit, remaining, resetAt(field("reset"), now), true
}

// When a reset value names: an RFC 3339 time, a duration such as 6m0s or 7.66s, or a number of seconds, unix seconds or unix milliseconds
func resetAt(v string, now time.Time) time.Time {
	if v == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t
	}
	if d, err := time.ParseDuration(v); err == nil {
		return now.Add(d)
	}
	n, err := strconv.ParseFloat(v, 64)
	switch {
	case err != nil || n < 0:
		return time.Time{}
	case n > 1e12:
		return time.UnixMilli(int64(n))
	case n > 1e9:
		return time.Unix(int64(n), 0)
	}
	return now.Add(time.Duration(n * float64(time.Second)))
}

// The kinds of credential the provider takes
func (s *SignIn) Kinds() []v1.CredentialKind {
	var out []v1.CredentialKind
	if s.Key || s.OAuth != nil && s.OAuth.Keyed != nil {
		out = append(out, v1.CredentialKind_CREDENTIAL_KIND_KEY)
	}
	if s.OAuth != nil && s.OAuth.Keyed == nil {
		out = append(out, v1.CredentialKind_CREDENTIAL_KIND_SIGN_IN)
	}
	return out
}

// The headers a key or access token signs requests with, beside the fixed ones the client did not send
func (s *SignIn) Headers(secret string, sent http.Header) map[string]string {
	out := map[string]string{s.Header: strings.TrimSpace(s.Scheme + " " + secret)}
	for k, v := range s.Fixed {
		if sent.Get(k) == "" {
			out[k] = v
		}
	}
	return out
}

// The catalog of a provider serving models, nil for a catalog of weights
func ProviderOf(kind v1.SourceKind) *Catalog {
	if cat, ok := catalogs[kind]; ok && cat.SignIn != nil {
		return cat
	}
	return nil
}

type authorizerKey struct{}

// Carries the caller's headers for provider sources, by source id, to the sources reached under ctx
func WithAuthorizer(ctx context.Context, fn func(sourceID string) http.Header) context.Context {
	return context.WithValue(ctx, authorizerKey{}, fn)
}

// The caller's headers for a provider source, nil when ctx carries none for it
func callerHeaders(ctx context.Context, sourceID string) http.Header {
	if fn, ok := ctx.Value(authorizerKey{}).(func(string) http.Header); ok {
		return fn(sourceID)
	}
	return nil
}

// What a provider's model is stored as in place of weights
type Reference struct {
	Endpoint string `json:"endpoint"`
	API      string `json:"api"`
	Model    string `json:"model"`
	// Tokens of context the provider reports, zero when it reports none
	Context uint64 `json:"context,omitempty"`
}

// The name of the reference file for models served over an API
func ReferenceFile(api v1.ApiFlavor) string { return "relay." + text.Enum(api) + ".json" }

// Reads a reference file, refusing one that names no endpoint or model
func ParseReference(data []byte) (*Reference, error) {
	var r Reference
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Endpoint == "" || r.Model == "" {
		return nil, fmt.Errorf("reference names no endpoint or model")
	}
	return &r, nil
}

const dataPrefix = "data:application/json;base64,"

// Models a provider serves, listed from its /models and stored as references
type modelsAPI struct{}

// One model as the providers list it, OpenAI's shape with each provider's extras, Anthropic's and ChatGPT's own
type modelEntry struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	Created     float64 `json:"created"`
	CreatedAt   string  `json:"created_at"`
	OwnedBy     string  `json:"owned_by"`
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name"`
	Description string  `json:"description"`
	// Context in tokens: Groq and DeepSeek, xAI and OpenRouter, Mistral, Anthropic
	// Whether ChatGPT's listing shows the model, list for one meant to be picked
	Visibility       string `json:"visibility"`
	ContextWindow    uint64 `json:"context_window"`
	ContextLength    uint64 `json:"context_length"`
	MaxContextLength uint64 `json:"max_context_length"`
	MaxInputTokens   uint64 `json:"max_input_tokens"`
}

// A listing page: data, ChatGPT's models, Anthropic's has_more and last_id, OpenRouter's next link
type modelPage struct {
	Data    []modelEntry `json:"data"`
	Models  []modelEntry `json:"models"`
	HasMore bool         `json:"has_more"`
	LastID  string       `json:"last_id"`
	Links   struct {
		Next string `json:"next"`
	} `json:"links"`
}

// Pages of a listing read before giving up
const modelPages = 20

func (e modelEntry) context() uint64 {
	return cmp.Or(e.ContextWindow, e.ContextLength, e.MaxContextLength, e.MaxInputTokens)
}

// Every model the provider lists to the caller
func listModels(ctx context.Context, c *Client) ([]modelEntry, error) {
	if callerHeaders(ctx, c.ID()) == nil {
		return nil, fmt.Errorf("%w for %s, add one under Profile", ErrNoCredential, c.cat.Name)
	}
	var out []modelEntry
	next, query := c.URL("models"), url.Values{}
	if c.cat.Speaks == v1.ApiFlavor_API_FLAVOR_ANTHROPIC {
		query.Set("limit", "1000")
	}
	for range modelPages {
		data, err := c.HTTP().read(ctx, next, query, http.Header{"Accept": {"application/json"}}, 32<<20)
		if err != nil {
			return nil, err
		}
		var page modelPage
		// Mistral's example answers a bare array
		if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '[' {
			err = json.Unmarshal(trimmed, &page.Data)
		} else {
			err = json.Unmarshal(data, &page)
		}
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", next, err)
		}
		for _, e := range append(page.Data, page.Models...) {
			if e.Visibility != "" && e.Visibility != "list" {
				continue
			}
			e.ID = cmp.Or(e.ID, e.Slug)
			out = append(out, e)
		}
		switch {
		case page.HasMore && page.LastID != "":
			query.Set("after_id", page.LastID)
		case page.Links.Next != "" && page.Links.Next != next:
			// The link carries its own query, a later page's after_id joining it
			next, query = c.HTTP().Absolute(page.Links.Next), url.Values{}
		default:
			return out, nil
		}
	}
	return out, nil
}

// A listed model as a hit, a language model in the format of the provider's API
func (c *Client) modelHit(e modelEntry) *v1.SearchHit {
	author, _ := splitRepo(e.ID)
	h := newHit(e.ID, cmp.Or(e.DisplayName, e.Name, e.ID), cmp.Or(e.OwnedBy, author, c.cat.ID))
	h.Description = Summary(e.Description)
	h.Task = "text-generation"
	h.Formats = []string{text.Enum(c.cat.Speaks)}
	if e.Created > 0 {
		h.CreatedAt = timestamppb.New(time.Unix(int64(e.Created), 0))
	} else {
		h.CreatedAt = Stamp(e.CreatedAt)
	}
	if n := e.context(); n > 0 {
		h.Extra["context"] = strconv.FormatUint(n, 10)
	}
	return h
}

func (modelsAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	if !AdmitsFormats(req, []string{text.Enum(c.cat.Speaks)}) || !AdmitsKind(req, v1.ModelKind_MODEL_KIND_LANGUAGE) {
		return &v1.SearchResponse{}, nil
	}
	list, err := listModels(ctx, c)
	if err != nil {
		return nil, err
	}
	hits := make([]*v1.SearchHit, 0, len(list))
	for _, e := range list {
		if e.ID != "" {
			hits = append(hits, c.modelHit(e))
		}
	}
	return localPage(hits, req, sort, c.Limit(req)), nil
}

// One reference file naming the endpoint, the API and the model, with the context the listing reports when the caller can list
func (modelsAPI) Resolve(ctx context.Context, c *Client, repo, _ string) (*v1.Model, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return nil, fmt.Errorf("%w: empty model id", ErrSource)
	}
	ref := Reference{Endpoint: c.Base(), API: text.Enum(c.cat.Speaks), Model: repo}
	if callerHeaders(ctx, c.ID()) != nil {
		list, err := listModels(ctx, c)
		if err != nil {
			return nil, err
		}
		found := false
		for _, e := range list {
			if e.ID == repo {
				found, ref.Context = true, e.context()
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%s lists no model %q", c.cat.Name, repo)
		}
	}
	data, err := json.MarshalIndent(ref, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	sum := sha256.Sum256(data)
	return &v1.Model{Repo: repo, Artifacts: []*v1.Artifact{{
		Path:      ReferenceFile(c.cat.Speaks),
		SizeBytes: uint64(len(data)),
		Sha256:    hex.EncodeToString(sum[:]),
		Url:       dataPrefix + base64.StdEncoding.EncodeToString(data),
	}}}, nil
}

// The reference's bytes, kept in the artifact's link since they were made
func (modelsAPI) Open(_ context.Context, _ *Client, _ *v1.Model, a *v1.Artifact) (Blob, error) {
	raw, ok := strings.CutPrefix(a.GetUrl(), dataPrefix)
	if !ok {
		return nil, fmt.Errorf("%s: resolve the model again", a.GetPath())
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", a.GetPath(), err)
	}
	return &memBlob{Reader: bytes.NewReader(data)}, nil
}

// Bytes held in memory as a blob
type memBlob struct{ *bytes.Reader }

func (b *memBlob) Close() error { return nil }

// Columns every provider of models shows
func modelColumns() []*v1.HitColumn {
	return append(Shown(ExtraCol("context", "Context", AsCount, "Tokens of context the provider reports")), Col(ColAuthor), Col(ColCreated))
}

// A catalog of models a provider serves over an API at an endpoint, signed in to with a key in a header
func modelCatalog(kind v1.SourceKind, id, name, endpoint string, api v1.ApiFlavor, signIn *SignIn) *Catalog {
	return &Catalog{
		ID:            id,
		Kind:          kind,
		Name:          name,
		Transports:    []Use{{Kind: TransportHTTP, Fields: map[string]string{"endpoint": endpoint}}},
		AuthRequired:  true,
		Description:   "Models " + name + " serves, run through the relay with your own credential",
		RepoExample:   "model-id",
		RepoPattern:   `^\S+$`,
		RevisionLabel: "revision",
		Sorts:         []string{SortCreated, SortName},
		Reversible:    []string{SortCreated, SortName},
		Columns:       modelColumns(),
		DefaultLimit:  50,
		MaxLimit:      500,
		Speaks:        api,
		SignIn:        signIn,
		API:           modelsAPI{},
	}
}

// A key sent as a bearer token, as the OpenAI API and most like it take one
func bearerKey() *SignIn { return &SignIn{Header: "Authorization", Scheme: "Bearer", Key: true} }
