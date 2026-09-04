package sources

import (
	"context"
	"fmt"
	"sort"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// What one provider tells the client: what it is called, which transports it
// moves bytes through with what defaults, what it accepts, and through API
// how its wire format maps onto the shared model. The client does everything
// else, so a provider file holds nothing but these facts and that mapping.
type Catalog struct {
	// Source id the seeded default goes by, and the provider's own name
	ID string
	// Kind the proto knows it as
	Kind v1.SourceKind
	// What people call the provider
	Name string
	// What people call the seeded default when it is a particular site, Docker Hub for an OCI registry
	Seed string
	// Settings the seeded default starts with beyond the provider defaults, the site's own endpoints
	SeedConfig map[string]string
	// The site people browse, when it is not the primary endpoint
	Web string
	// Path of the browse page under the site, such as /models
	WebPath string
	// Transports in order, the first is primary and owns the bare field names
	Transports []Use
	// Provider settings beyond what its transports read
	Extra []*v1.ConfigField
	// Set when downloads need a key
	AuthRequired bool
	// Set for providers that exist only through config, such as a directory or a mirror
	Configured bool
	// Set when the provider cannot list without a query, or cannot search at all
	NoBrowse, NoSearch bool
	// Housekeeping tags the provider attaches that say nothing about a model, patterns matched whole
	Noise []string
	// Extras of a hit worth a chip on its card, in order
	HitFields []*v1.ConfigField

	Description   string
	RepoExample   string
	RepoPattern   string
	RevisionLabel string

	// Sort ids in display order, the first is the default
	Sorts []string
	// Sort ids the provider can flip, most only order descending
	Reversible []string
	// Facets fixed for the life of the provider, API.Facets adds live ones before them
	Facets []*v1.Facet
	// Page size when the request names none, and the largest the provider serves
	DefaultLimit, MaxLimit int

	API API
}

// How a provider's wire format maps onto the shared model
//
// Every method receives the client so it can reach the transports and the
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

// Implemented by an API that can list only when the source names a transport
type Browser interface {
	Browses(c *Client) bool
}

// API whose facets come from the catalog itself, read on demand and kept for a while
type Faceter interface {
	Facets(ctx context.Context, c *Client) ([]*v1.Facet, error)
}

// API that checks a built client, for settings that go together or auth it sets up itself
type Checker interface {
	Check(c *Client) error
}

var catalogs = map[v1.SourceKind]*Catalog{}

// Adds a provider to the list, each file registers its own
func register(cat *Catalog) {
	if _, dup := catalogs[cat.Kind]; dup {
		panic(fmt.Sprintf("catalog %s registered twice", cat.Kind))
	}
	if len(cat.Transports) == 0 || cat.Transports[0].Name != "" {
		panic(fmt.Sprintf("catalog %s: the first transport is primary and has no name", cat.ID))
	}
	if len(cat.Sorts) == 0 {
		panic(fmt.Sprintf("catalog %s: needs a sort, the first is the default", cat.ID))
	}
	seen := map[string]bool{}
	for _, u := range cat.Transports {
		if _, ok := transportConstructors[u.Kind]; !ok {
			panic(fmt.Sprintf("catalog %s: unknown transport %q", cat.ID, u.Kind))
		}
		if seen[u.key()] {
			panic(fmt.Sprintf("catalog %s: transport %s needs a distinct name", cat.ID, u.Kind))
		}
		seen[u.key()] = true
	}
	names := map[string]bool{}
	for _, f := range cat.Fields() {
		if names[f.GetName()] {
			panic(fmt.Sprintf("catalog %s: setting %s declared twice", cat.ID, f.GetName()))
		}
		names[f.GetName()] = true
	}
	if cat.Name == "" {
		cat.Name = cat.ID
	}
	catalogs[cat.Kind] = cat
}

// Every provider in kind order
func all() []*Catalog {
	out := make([]*Catalog, 0, len(catalogs))
	for _, cat := range catalogs {
		out = append(out, cat)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// The settings a source of this provider accepts, transports first in order, then the provider's own
func (cat *Catalog) Fields() []*v1.ConfigField {
	var out []*v1.ConfigField
	for _, u := range cat.Transports {
		out = append(out, u.fields()...)
	}
	return append(out, cat.Extra...)
}

// The keys of the transports in order
func (cat *Catalog) transportNames() []string {
	out := make([]string, 0, len(cat.Transports))
	for _, u := range cat.Transports {
		out = append(out, u.key())
	}
	return out
}

// What the seeded default is called
func (cat *Catalog) seedName() string {
	if cat.Seed != "" {
		return cat.Seed
	}
	return cat.Name
}

// Describes the provider for the API
func (cat *Catalog) Provider() *v1.Provider {
	return &v1.Provider{Kind: cat.Kind, Name: cat.Name, Description: cat.Description, Fields: cat.Fields(), Transports: cat.transportNames(), Configured: cat.Configured}
}

// Builds a primary HTTP use with an endpoint and the token variable it reads, empty for none
func httpUse(endpoint, tokenEnv string) Use {
	return Use{Kind: TransportHTTP, Fields: map[string]string{"endpoint": endpoint, "token_env": tokenEnv}}
}
