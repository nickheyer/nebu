package sources

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	facetTTL     = 6 * time.Hour
	cardMax      = 2 << 20
	defaultLimit = 30
	maxLimit     = 100
)

// What one catalog tells the client: where it lives, what it accepts, and
// through API how its wire format maps onto the shared model. The client does
// everything else, so a catalog file holds nothing but these facts.
type Catalog struct {
	// Source id commands and the UI name it by
	ID string
	// Kind the proto knows it as
	Kind v1.SourceKind
	// The API host
	Endpoint string
	// The site people browse, when it is not the API host
	Web string
	// Path of the browse page under the site, such as /models
	WebPath string
	// A distribution registry beside the API, for catalogs that ship layers
	Registry string
	// Environment variable holding the API key
	TokenEnv string
	// Environment variable holding the registry credential as user:secret
	RegistryTokenEnv string
	// Environment variable holding the user name, for a key that travels as basic auth
	UsernameEnv string
	// Set when downloads need a key
	AuthRequired bool
	// Set for kinds that exist only through config, such as a directory or a mirror
	Configured bool

	Description   string
	RepoExample   string
	RepoPattern   string
	RevisionLabel string

	// Sort ids in display order, the first is the default
	Sorts []string
	// Sort ids the catalog can flip, most only order descending
	Reversible []string
	// Facets fixed for the life of the catalog, API.Facets adds live ones before them
	Facets []*v1.Facet
	// Page size when the request names none, and the largest the catalog serves
	DefaultLimit, MaxLimit int

	API API
}

// How a catalog's wire format maps onto the shared model
//
// Every method receives the client so it can reach the catalog's hosts and the
// shared helpers. Hits and models come back without a source id, the client
// stamps it. The client has already checked the sort against the catalog.
type API interface {
	Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error)
	Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error)
	Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error)
}

// An ordering the client checked against the catalog's sorts
type Sort struct {
	ID        string
	Ascending bool
}

// API that lists branches, tags, versions, or variants
type Reviser interface {
	Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error)
}

// API that serves a description for a repository
type Carder interface {
	Card(ctx context.Context, c *Client, repo, revision string) (*v1.ModelCard, error)
}

// API whose facets come from the catalog itself, read on demand and kept for a while
type Faceter interface {
	Facets(ctx context.Context, c *Client) ([]*v1.Facet, error)
}

// API that authorizes requests itself instead of sending the key as a bearer
type Authorizer interface {
	Headers(ctx context.Context, c *Client) http.Header
}

// API that needs something from config before it can run
type Checker interface {
	Check(c *Client) error
}

var catalogs = map[v1.SourceKind]*Catalog{}

// Adds a catalog to the source list, each file registers its own
func register(cat *Catalog) {
	if _, dup := catalogs[cat.Kind]; dup {
		panic(fmt.Sprintf("catalog %s registered twice", cat.Kind))
	}
	catalogs[cat.Kind] = cat
}

// Every catalog in kind order
func all() []*Catalog {
	out := make([]*Catalog, 0, len(catalogs))
	for _, cat := range catalogs {
		out = append(out, cat)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// The one implementation of Source: a catalog's facts, the hosts they name,
// and the behaviour every catalog shares
type Client struct {
	cat      *Catalog
	spec     *v1.Source
	http     *HTTP
	dist     *Distribution
	web      string
	token    string
	tokenEnv string
	facets   Memo[[]*v1.Facet]
}

// Builds a client for a catalog; spec carries the id and, for configured kinds, the endpoint or path
func newClient(cat *Catalog, spec *v1.Source) (*Client, error) {
	c := &Client{cat: cat, spec: spec, facets: Memo[[]*v1.Facet]{TTL: facetTTL}}
	c.tokenEnv = cat.TokenEnv
	if c.tokenEnv == "" {
		c.tokenEnv = cat.RegistryTokenEnv
	}
	if spec.GetTokenEnv() != "" {
		c.tokenEnv = spec.GetTokenEnv()
	}
	if c.tokenEnv != "" {
		c.token = os.Getenv(c.tokenEnv)
	}
	endpoint := cat.Endpoint
	if spec.GetEndpoint() != "" {
		endpoint = spec.GetEndpoint()
	}
	if endpoint != "" {
		bearer := c.token
		if _, ok := cat.API.(Authorizer); ok || (cat.TokenEnv == "" && spec.GetTokenEnv() == "") {
			bearer = ""
		}
		h, err := NewHTTP(endpoint, bearer)
		if err != nil {
			return nil, err
		}
		if cat.UsernameEnv != "" && bearer != "" {
			if user := os.Getenv(cat.UsernameEnv); user != "" {
				h.UseBasic(user)
			}
		}
		c.http = h
	}
	c.web = strings.TrimRight(cat.Web, "/")
	if c.web == "" && c.http != nil {
		c.web = c.http.Base()
	}
	if cat.Registry != "" {
		creds := ""
		if cat.RegistryTokenEnv != "" {
			creds = os.Getenv(cat.RegistryTokenEnv)
		}
		d, err := NewDistribution(cat.Registry, creds)
		if err != nil {
			return nil, err
		}
		c.dist = d
	}
	if ch, ok := cat.API.(Checker); ok {
		if err := ch.Check(c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *Client) Spec() *v1.Source { return c.spec }

// The source id
func (c *Client) ID() string { return c.spec.GetId() }

// The catalog this client speaks for
func (c *Client) Catalog() *Catalog { return c.cat }

// The API key from the environment, empty when none is set
func (c *Client) Token() string { return c.token }

// Reports whether a key is set
func (c *Client) HasToken() bool { return c.token != "" }

// The API host without a trailing slash, empty for a directory
func (c *Client) Base() string {
	if c.http == nil {
		return ""
	}
	return c.http.Base()
}

// Joins escaped path segments onto the API host
func (c *Client) URL(segments ...string) string { return c.http.URL(segments...) }

// The site people browse
func (c *Client) Web() string { return c.web }

// Joins escaped path segments onto the site
func (c *Client) Page(segments ...string) string { return c.web + joinPath(segments...) }

// The page the catalog is browsed at, the directory for one on disk
func (c *Client) WebURL() string {
	if c.web != "" {
		return c.web + c.cat.WebPath
	}
	return c.spec.GetPath()
}

// The distribution registry beside the API, nil when the catalog has none
func (c *Client) Distribution() *Distribution { return c.dist }

// The plain HTTP client for the API host, nil for a directory
func (c *Client) HTTP() *HTTP { return c.http }

// Headers the API adds to every request
func (c *Client) headers(ctx context.Context) http.Header {
	if a, ok := c.cat.API.(Authorizer); ok {
		return a.Headers(ctx, c)
	}
	return nil
}

// Sends a request to the API host with the catalog's auth
func (c *Client) Do(ctx context.Context, method, rawURL string, query url.Values, header http.Header) (*http.Response, error) {
	h := c.headers(ctx)
	for k, vs := range header {
		if h == nil {
			h = http.Header{}
		}
		h[k] = vs
	}
	return c.http.Do(ctx, method, rawURL, query, h)
}

// Fetches JSON from the API host into out and returns response headers
func (c *Client) JSON(ctx context.Context, rawURL string, query url.Values, out any) (http.Header, error) {
	return c.http.JSONWith(ctx, http.MethodGet, rawURL, query, c.headers(ctx), nil, out)
}

// Sends an optional JSON body to the API host and decodes the JSON answer
func (c *Client) JSONBody(ctx context.Context, method, rawURL string, query url.Values, body any, out any) (http.Header, error) {
	return c.http.JSONWith(ctx, method, rawURL, query, c.headers(ctx), body, out)
}

// Fetches a text body from the API host, capped
func (c *Client) Text(ctx context.Context, rawURL string, query url.Values, max int64) (string, error) {
	return c.http.TextWith(ctx, rawURL, query, c.headers(ctx), max)
}

// Fetches a markdown card, an empty card with the page link when the catalog has none
func (c *Client) CardText(ctx context.Context, rawURL string, query url.Values, pageURL string) (*v1.ModelCard, error) {
	text, err := c.Text(ctx, rawURL, query, cardMax)
	if err != nil {
		if IsStatus(err, http.StatusNotFound) {
			return &v1.ModelCard{Url: pageURL}, nil
		}
		return nil, err
	}
	return &v1.ModelCard{Markdown: text, Url: pageURL}, nil
}

// Clamps the requested page size to the catalog's limits
func (c *Client) Limit(req *v1.SearchRequest) int {
	def, max := c.cat.DefaultLimit, c.cat.MaxLimit
	if def <= 0 {
		def = defaultLimit
	}
	if max <= 0 {
		max = maxLimit
	}
	return Limit(req, def, max)
}

// Opens an artifact served by the API host as a range readable blob, refusing one whose size is unknown
func (c *Client) Range(rawURL string, a *v1.Artifact) (Blob, error) {
	if a.GetSizeBytes() == 0 {
		return nil, fmt.Errorf("%s: unknown size", a.GetPath())
	}
	b := NewRangeBlob(c.http, rawURL, int64(a.GetSizeBytes()))
	if auth, ok := c.cat.API.(Authorizer); ok {
		b.WithHeaderFunc(func(ctx context.Context) (http.Header, error) { return auth.Headers(ctx, c), nil })
	}
	return b, nil
}

func (c *Client) Capabilities(ctx context.Context) *v1.SourceCapabilities {
	facets := c.cat.Facets
	if f, ok := c.cat.API.(Faceter); ok {
		live, _ := c.facets.Get(ctx, func(ctx context.Context) ([]*v1.Facet, error) { return f.Facets(ctx, c) })
		facets = append(append([]*v1.Facet{}, live...), c.cat.Facets...)
	}
	_, revisions := c.cat.API.(Reviser)
	_, card := c.cat.API.(Carder)
	return &v1.SourceCapabilities{
		Browse:        true,
		Search:        true,
		Paginate:      true,
		Card:          card,
		Revisions:     revisions,
		AuthRequired:  c.cat.AuthRequired,
		TokenPresent:  c.token != "",
		TokenEnv:      c.tokenEnv,
		Sorts:         c.sorts(),
		DefaultSort:   c.cat.Sorts[0],
		Facets:        facets,
		RepoExample:   c.cat.RepoExample,
		RepoPattern:   c.cat.RepoPattern,
		WebUrl:        c.WebURL(),
		Description:   c.cat.Description,
		RevisionLabel: c.cat.RevisionLabel,
	}
}

// The catalog's sorts with the shared labels, marked where ascending is accepted
func (c *Client) sorts() []*v1.SortOption {
	out := Sorts(c.cat.Sorts...)
	for _, s := range out {
		s.Reversible = slices.Contains(c.cat.Reversible, s.GetId())
	}
	return out
}

// Checks the requested order against the catalog, the first sort standing in for none
func (c *Client) sort(req *v1.SearchRequest) (Sort, error) {
	id := req.GetSort()
	if id == "" {
		id = c.cat.Sorts[0]
	}
	if !slices.Contains(c.cat.Sorts, id) {
		return Sort{}, fmt.Errorf("sort %q: %w", id, ErrUnsupported)
	}
	if req.GetAscending() && !slices.Contains(c.cat.Reversible, id) {
		return Sort{}, fmt.Errorf("ascending %s: %w", id, ErrUnsupported)
	}
	return Sort{ID: id, Ascending: req.GetAscending()}, nil
}

func (c *Client) Search(ctx context.Context, req *v1.SearchRequest) (*v1.SearchResponse, error) {
	sort, err := c.sort(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.cat.API.Search(ctx, c, req, sort)
	if err != nil {
		return nil, err
	}
	for _, h := range resp.GetHits() {
		if h.SourceId == "" {
			h.SourceId = c.ID()
		}
	}
	return resp, nil
}

func (c *Client) Resolve(ctx context.Context, repo, revision string) (*v1.Model, error) {
	model, err := c.cat.API.Resolve(ctx, c, repo, revision)
	if err != nil {
		return nil, err
	}
	model.SourceId = c.ID()
	if model.ResolvedAt == nil {
		model.ResolvedAt = timestamppb.Now()
	}
	return model, nil
}

// Lists revisions with the default first, ErrUnsupported when the catalog has none
func (c *Client) Revisions(ctx context.Context, repo string) ([]*v1.Revision, error) {
	r, ok := c.cat.API.(Reviser)
	if !ok {
		return nil, fmt.Errorf("revisions: %w", ErrUnsupported)
	}
	out, err := r.Revisions(ctx, c, repo)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, func(a, b *v1.Revision) int {
		switch {
		case a.GetDefault() == b.GetDefault():
			return 0
		case a.GetDefault():
			return -1
		}
		return 1
	})
	return out, nil
}

// Fetches the model card, ErrUnsupported when the catalog has none
func (c *Client) Card(ctx context.Context, repo, revision string) (*v1.ModelCard, error) {
	cd, ok := c.cat.API.(Carder)
	if !ok {
		return nil, fmt.Errorf("model card: %w", ErrUnsupported)
	}
	return cd.Card(ctx, c, repo, revision)
}

func (c *Client) Open(ctx context.Context, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	return c.cat.API.Open(ctx, c, model, artifact)
}
