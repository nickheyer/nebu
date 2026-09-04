package sources

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
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

// The one implementation of Source: a provider's facts, the transports built
// from a source's settings, and the behaviour every provider shares
type Client struct {
	cat        *Catalog
	spec       *v1.Source
	cfg        map[string]string
	transports map[string]Transport
	facets     Memo[[]*v1.Facet]
	cache      sync.Map
}

// Keeps a value for the life of the client, built on first use, so a
// provider's per repository state never crosses sources
func Cached[T any](c *Client, key string, build func() T) T {
	if v, ok := c.cache.Load(key); ok {
		return v.(T)
	}
	v, _ := c.cache.LoadOrStore(key, build())
	return v.(T)
}

// Builds a client for a provider from a source's settings over the provider's defaults
func newClient(cat *Catalog, spec *v1.Source, cacheDir string) (*Client, error) {
	cfg, err := resolveConfig(cat.Fields(), spec.GetConfig())
	if err != nil {
		return nil, err
	}
	c := &Client{cat: cat, spec: spec, cfg: cfg, transports: map[string]Transport{}, facets: Memo[[]*v1.Facet]{TTL: facetTTL}}
	for _, u := range cat.Transports {
		values := make(map[string]string, len(u.Fields))
		for name := range u.Fields {
			values[name] = cfg[u.prefix()+name]
		}
		for _, name := range u.Inherit {
			values[name] = cfg[name]
		}
		t, err := transportConstructors[u.Kind](values, transportEnv{cacheDir: cacheDir, use: u.key()})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", u.key(), err)
		}
		if t != nil {
			c.transports[u.key()] = t
		}
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

// The provider this client speaks for
func (c *Client) Catalog() *Catalog { return c.cat }

// One effective setting by full name, the provider default when the source set none
func (c *Client) Config(name string) string { return c.cfg[name] }

// The transport of one use by key, nil when the source did not enable it
func (c *Client) Transport(use string) Transport { return c.transports[use] }

// The first transport of a kind, nil when the provider has none
func transportOf[T Transport](c *Client, kind string) T {
	var zero T
	for _, u := range c.cat.Transports {
		if u.Kind == kind {
			if t, ok := c.transports[u.key()].(T); ok {
				return t
			}
		}
	}
	return zero
}

// The plain HTTP transport for the API host, nil for a directory
func (c *Client) HTTP() *HTTP { return transportOf[*HTTP](c, TransportHTTP) }

// The distribution registry beside the API, nil when the provider has none
func (c *Client) Distribution() *Distribution {
	return transportOf[*Distribution](c, TransportDistribution)
}

// The filesystem the source reads, nil when the provider has none
func (c *Client) File() *File { return transportOf[*File](c, TransportFile) }

// The git transport, nil when the provider has none
func (c *Client) Git() *Git { return transportOf[*Git](c, TransportGit) }

// The Hugging Face CLI, nil unless the source turned it on
func (c *Client) CLI() *HFCLI { return transportOf[*HFCLI](c, TransportHFCLI) }

// The API key from the environment variable the primary transport names, empty when none is set
func (c *Client) Token() string {
	if v := c.cfg[c.tokenSetting()]; v != "" {
		return os.Getenv(v)
	}
	return ""
}

// The setting naming the key's variable, the bare one or the first transport's own
func (c *Client) tokenSetting() string {
	first := ""
	for _, f := range c.cat.Fields() {
		if f.GetName() == "token_env" {
			return f.GetName()
		}
		if first == "" && f.GetType() == v1.ConfigType_CONFIG_TYPE_ENV && strings.HasSuffix(f.GetName(), "token_env") {
			first = f.GetName()
		}
	}
	return first
}

// Reports whether a key is set
func (c *Client) HasToken() bool { return c.Token() != "" }

// The API host without a trailing slash, empty for a directory
func (c *Client) Base() string {
	if h := c.HTTP(); h != nil {
		return h.Base()
	}
	return ""
}

// Joins escaped path segments onto the API host
func (c *Client) URL(segments ...string) string { return c.HTTP().URL(segments...) }

// The site people browse
func (c *Client) Web() string {
	if c.cat.Web != "" {
		return strings.TrimRight(c.cat.Web, "/")
	}
	return c.Base()
}

// Joins escaped path segments onto the site
func (c *Client) Page(segments ...string) string { return c.Web() + joinPath(segments...) }

// The page the provider is browsed at, the directory for one on disk
func (c *Client) WebURL() string {
	if web := c.Web(); web != "" {
		return web + c.cat.WebPath
	}
	if f := c.File(); f != nil {
		return f.Root()
	}
	return ""
}

// Fetches JSON from the API host into out and returns response headers
func (c *Client) JSON(ctx context.Context, rawURL string, query url.Values, out any) (http.Header, error) {
	return c.HTTP().JSON(ctx, rawURL, query, out)
}

// Sends an optional JSON body to the API host and decodes the JSON answer
func (c *Client) JSONBody(ctx context.Context, method, rawURL string, query url.Values, body any, out any) (http.Header, error) {
	return c.HTTP().JSONBody(ctx, method, rawURL, query, body, out)
}

// Fetches a text body from the API host, capped
func (c *Client) Text(ctx context.Context, rawURL string, query url.Values, max int64) (string, error) {
	return c.HTTP().Text(ctx, rawURL, query, max)
}

// Fetches a markdown card, an empty card with the page link when the provider has none
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

// Clamps the requested page size to the provider's limits
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

// Opens an artifact served by the API host as a range readable blob
func (c *Client) Range(ctx context.Context, rawURL string, a *v1.Artifact) (Blob, error) {
	return c.HTTP().Open(ctx, rawURL, int64(a.GetSizeBytes()))
}

func (c *Client) Capabilities(ctx context.Context) *v1.SourceCapabilities {
	facets := c.cat.Facets
	if f, ok := c.cat.API.(Faceter); ok {
		live, _ := c.facets.Get(ctx, func(ctx context.Context) ([]*v1.Facet, error) { return f.Facets(ctx, c) })
		facets = append(append([]*v1.Facet{}, live...), c.cat.Facets...)
	}
	_, revisions := c.cat.API.(Reviser)
	_, card := c.cat.API.(Carder)
	// A provider may need a transport this source did not name before it can list anything
	lists := true
	if b, ok := c.cat.API.(Browser); ok {
		lists = b.Browses(c)
	}
	return &v1.SourceCapabilities{
		Browse:        !c.cat.NoBrowse && lists,
		Search:        !c.cat.NoSearch && lists,
		Paginate:      true,
		Card:          card && lists,
		Revisions:     revisions,
		AuthRequired:  c.cat.AuthRequired,
		TokenPresent:  c.HasToken(),
		TokenEnv:      c.cfg[c.tokenSetting()],
		Endpoint:      c.cfg["endpoint"],
		Sorts:         c.sorts(),
		DefaultSort:   c.cat.Sorts[0],
		Facets:        facets,
		RepoExample:   c.cat.RepoExample,
		RepoPattern:   c.cat.RepoPattern,
		WebUrl:        c.WebURL(),
		Description:   c.cat.Description,
		RevisionLabel: c.cat.RevisionLabel,
		Name:          c.cat.Name,
		Fields:        c.cat.Fields(),
		Transports:    c.cat.transportNames(),
		HiddenTags:    c.cat.Noise,
		HitFields:     c.cat.HitFields,
	}
}

// The provider's sorts with the shared labels, marked where ascending is accepted
func (c *Client) sorts() []*v1.SortOption {
	out := Sorts(c.cat.Sorts...)
	for _, s := range out {
		s.Reversible = slices.Contains(c.cat.Reversible, s.GetId())
	}
	return out
}

// Checks the requested order against the provider, the first sort standing in for none
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

// Lists revisions with the default first, ErrUnsupported when the provider has none
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

// Fetches the model card, ErrUnsupported when the provider has none
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
