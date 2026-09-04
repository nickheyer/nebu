package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	tokenGrace = 30 * time.Second
	tokenTTL   = 5 * time.Minute
	tagsPage   = "1000"
)

// Manifest media types a registry may answer with, preferred first
var manifestAccept = strings.Join([]string{
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
}, ", ")

// One blob a manifest points at
type Descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations"`
	Platform    *struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
	} `json:"platform"`
}

// An image or artifact manifest, or an index of them
type Manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
	Manifests     []Descriptor      `json:"manifests"`
	Annotations   map[string]string `json:"annotations"`
	// Digest of the manifest itself, from the registry
	Digest string `json:"-"`
}

// Reports whether this is an index pointing at platform manifests
func (m *Manifest) IsIndex() bool {
	return len(m.Manifests) > 0 && len(m.Layers) == 0
}

type bearer struct {
	value   string
	expires time.Time
}

// Reads an OCI distribution registry, where some catalogs ship models as layers,
// answering bearer challenges anonymously or with a credential
type Distribution struct {
	http   *HTTP
	creds  string
	mu     sync.Mutex
	tokens map[string]bearer
}

// Builds a reader for a registry endpoint, creds is user:secret for its token service
func NewDistribution(endpoint, creds string) (*Distribution, error) {
	h, err := NewHTTP(endpoint, "")
	if err != nil {
		return nil, err
	}
	return &Distribution{http: h, creds: creds, tokens: map[string]bearer{}}, nil
}

// Returns the endpoint without a trailing slash
func (d *Distribution) Base() string { return d.http.Base() }

var challengeRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// Parses a Bearer challenge into its parameters
func parseChallenge(h string) map[string]string {
	if !strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return nil
	}
	out := map[string]string{}
	for _, m := range challengeRe.FindAllStringSubmatch(h[len("bearer "):], -1) {
		out[m[1]] = m[2]
	}
	return out
}

// Returns a cached token whose scope covers the repo, or any unscoped one
func (d *Distribution) tokenFor(repo string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for scope, t := range d.tokens {
		if now.Before(t.expires) && (scope == "" || strings.Contains(scope, "repository:"+repo+":")) {
			return t.value
		}
	}
	return ""
}

// Names the repo a v2 URL addresses, for picking a scoped token
func repoOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	p := strings.TrimPrefix(u.Path, "/v2/")
	for _, marker := range []string{"/manifests/", "/blobs/", "/tags/"} {
		if i := strings.Index(p, marker); i >= 0 {
			return p[:i]
		}
	}
	return ""
}

// Sends a request, answering a bearer challenge once and caching the token by scope
func (d *Distribution) do(ctx context.Context, method, rawURL string, header http.Header) (*http.Response, error) {
	if header == nil {
		header = http.Header{}
	}
	if tok := d.tokenFor(repoOf(rawURL)); tok != "" {
		header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := d.http.Do(ctx, method, rawURL, nil, header)
	if err == nil || !IsStatus(err, http.StatusUnauthorized) {
		return resp, err
	}
	var se *StatusError
	if !errors.As(err, &se) {
		return nil, err
	}
	params := parseChallenge(se.Header.Get("Www-Authenticate"))
	if params == nil || params["realm"] == "" {
		return nil, err
	}
	tok, err := d.fetchToken(ctx, params)
	if err != nil {
		return nil, err
	}
	header.Set("Authorization", "Bearer "+tok)
	return d.http.Do(ctx, method, rawURL, nil, header)
}

type tokenBody struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// Exchanges a challenge for a token at the realm the registry named
func (d *Distribution) fetchToken(ctx context.Context, params map[string]string) (string, error) {
	scope := params["scope"]
	d.mu.Lock()
	if t, ok := d.tokens[scope]; ok && time.Now().Before(t.expires) {
		d.mu.Unlock()
		return t.value, nil
	}
	d.mu.Unlock()
	q := url.Values{}
	if params["service"] != "" {
		q.Set("service", params["service"])
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	realm, err := url.Parse(params["realm"])
	if err != nil {
		return "", fmt.Errorf("token realm: %w", err)
	}
	if !realm.IsAbs() {
		return "", fmt.Errorf("token realm %q is not absolute", params["realm"])
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params["realm"]+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if d.creds != "" {
		user, pass, _ := strings.Cut(d.creds, ":")
		req.SetBasicAuth(user, pass)
	}
	resp, err := d.http.Send(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", &StatusError{Method: http.MethodGet, URL: params["realm"], Code: resp.StatusCode, Status: resp.Status, Body: "registry refused a token for " + scope}
	}
	var body tokenBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	value := body.Token
	if value == "" {
		value = body.AccessToken
	}
	if value == "" {
		return "", fmt.Errorf("token realm answered without a token")
	}
	ttl := tokenTTL
	if body.ExpiresIn > 0 {
		ttl = time.Duration(body.ExpiresIn) * time.Second
	}
	d.mu.Lock()
	d.tokens[scope] = bearer{value: value, expires: time.Now().Add(ttl - tokenGrace)}
	d.mu.Unlock()
	return value, nil
}

// Returns headers carrying a valid token for pulling one repo, none on anonymous registries
func (d *Distribution) PullHeaders(ctx context.Context, repo string) (http.Header, error) {
	h := http.Header{}
	if tok := d.tokenFor(repo); tok != "" {
		h.Set("Authorization", "Bearer "+tok)
		return h, nil
	}
	// A HEAD on the tags list is the cheapest way to learn the challenge, registries
	// without that endpoint answer 404 and need no token at all
	resp, err := d.do(ctx, http.MethodHead, d.http.URL("v2", repo, "tags", "list"), nil)
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Code != http.StatusUnauthorized && se.Code != http.StatusForbidden {
			return h, nil
		}
		return nil, err
	}
	resp.Body.Close()
	if tok := d.tokenFor(repo); tok != "" {
		h.Set("Authorization", "Bearer "+tok)
	}
	return h, nil
}

// Fetches a manifest by tag or digest, descending into an index for linux/amd64 or the first entry
func (d *Distribution) Manifest(ctx context.Context, repo, ref string) (*Manifest, error) {
	resp, err := d.do(ctx, http.MethodGet, d.http.URL("v2", repo, "manifests", ref), http.Header{"Accept": {manifestAccept}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest %s:%s: %w", repo, ref, err)
	}
	m.Digest = resp.Header.Get("Docker-Content-Digest")
	if m.IsIndex() {
		pick := m.Manifests[0]
		for _, desc := range m.Manifests {
			if desc.Platform != nil && desc.Platform.OS == "linux" && desc.Platform.Architecture == "amd64" {
				pick = desc
				break
			}
		}
		child, err := d.Manifest(ctx, repo, pick.Digest)
		if err != nil {
			return nil, err
		}
		if child.Digest == "" {
			child.Digest = pick.Digest
		}
		if len(child.Annotations) == 0 {
			child.Annotations = m.Annotations
		}
		return child, nil
	}
	return &m, nil
}

type tagList struct {
	Tags []string `json:"tags"`
}

// Lists every tag of a repo, following pagination
func (d *Distribution) Tags(ctx context.Context, repo string) ([]string, error) {
	next := d.http.URL("v2", repo, "tags", "list") + "?n=" + tagsPage
	var out []string
	for next != "" {
		resp, err := d.do(ctx, http.MethodGet, next, http.Header{"Accept": {"application/json"}})
		if err != nil {
			return nil, err
		}
		var body tagList
		err = json.NewDecoder(resp.Body).Decode(&body)
		link := resp.Header.Get("Link")
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("tags %s: %w", repo, err)
		}
		out = append(out, body.Tags...)
		next = NextLink(link, d.http.Base())
	}
	return out, nil
}

// Reads a small blob, such as a config, as JSON
func (d *Distribution) BlobJSON(ctx context.Context, repo, digest string, out any) error {
	resp, err := d.do(ctx, http.MethodGet, d.http.URL("v2", repo, "blobs", digest), http.Header{"Accept": {"application/json, */*"}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// Opens a layer as a range readable blob, refreshing the token as it expires
func (d *Distribution) Blob(repo, digest string, size int64) (Blob, error) {
	if size <= 0 {
		return nil, fmt.Errorf("%s: unknown size", digest)
	}
	return NewRangeBlob(d.http, d.http.URL("v2", repo, "blobs", digest), size).WithHeaderFunc(func(ctx context.Context) (http.Header, error) {
		return d.PullHeaders(ctx, repo)
	}), nil
}

// Strips the sha256: prefix from a digest, lower cased
func Hex(digest string) string {
	return strings.ToLower(strings.TrimPrefix(digest, "sha256:"))
}
