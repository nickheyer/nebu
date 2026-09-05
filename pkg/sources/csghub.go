package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// OpenCSG, the public CSGHub
var csghub = &Catalog{
	ID:            "csghub",
	Kind:          v1.SourceKind_SOURCE_KIND_CSGHUB,
	Name:          "CSGHub",
	Seed:          "OpenCSG",
	Transports:    []Use{httpUse("https://hub.opencsg.com", "OPENCSG_TOKEN")},
	Web:           "https://opencsg.com",
	WebPath:       "/models",
	Description:   "OpenCSG, the CSGHub community hub, or any CSGHub install",
	RepoExample:   "org/model",
	RepoPattern:   `^[\w.-]+/[\w.-]+$`,
	RevisionLabel: "revision",
	Sorts:         []string{SortTrending, SortDownloads, SortLikes, SortUpdated},
	SortKeys: map[string]string{
		SortTrending:  "trending",
		SortDownloads: "most_download",
		SortLikes:     "most_favorite",
		SortUpdated:   "recently_update",
	},
	Noise: []string{".+:.+", "[a-z]{2,3}"},
	HitFields: []*v1.ConfigField{
		{Name: "architecture", Label: "Architecture", Description: "Model architecture"},
		{Name: "base_model", Label: "Base model", Description: "Base model this was made from"},
	},
	API: csghubAPI{},
}

func init() { register(csghub) }

const (
	csgRevision = "main"
	csgMaxDepth = 8
)

// Listing query parameters by facet id
var csgFacetParams = map[string]string{
	FacetTask:      "task_tag",
	FacetFramework: "framework_tag",
	FacetLicense:   "license_tag",
}

// Tag categories the hub publishes, by the facet each fills
var csgTaxonomy = []taxonomy{{"task", FacetTask, "Task"}, {"framework", FacetFramework, "Framework"}, {"license", FacetLicense, "License"}}

// The CSGHub API: enveloped JSON, a paged list, branches, a tree, and Hub style downloads
type csghubAPI struct{}

// The hub wraps every answer in a data field
type csgEnvelope[T any] struct {
	Data T `json:"data"`
}

// Fetches one enveloped answer
func csgData[T any](ctx context.Context, c *Client, rawURL string, q url.Values) (T, error) {
	var body csgEnvelope[T]
	_, err := c.JSON(ctx, rawURL, q, &body)
	return body.Data, err
}

type csgTag struct {
	Name     string `json:"name"`
	Group    string `json:"group"`
	BuiltIn  bool   `json:"built_in"`
	ShowName string `json:"show_name"`
}

// Reads the hub's own tag taxonomy, keeping the built in entries
func (csghubAPI) Facets(ctx context.Context, c *Client) ([]*v1.Facet, error) {
	return taxonomyFacets(ctx, csgTaxonomy, func(ctx context.Context, category string) ([]*v1.FacetValue, error) {
		tags, err := csgData[[]csgTag](ctx, c, c.URL("api", "v1", "tags"), url.Values{"category": {category}, "scope": {"model"}})
		if err != nil {
			return nil, err
		}
		var values []*v1.FacetValue
		for _, e := range tags {
			if !e.BuiltIn || e.Name == "" {
				continue
			}
			values = append(values, GroupedValue(e.Name, e.ShowName, Humanize(e.Group)))
		}
		return values, nil
	})
}

type csgModel struct {
	Name        string `json:"name"`
	Nickname    string `json:"nickname"`
	Description string `json:"description"`
	Likes       int64  `json:"likes"`
	Downloads   int64  `json:"downloads"`
	Path        string `json:"path"`
	Private     bool   `json:"private"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	Tags        []struct {
		Name     string `json:"name"`
		Category string `json:"category"`
	} `json:"tags"`
	License       string `json:"license"`
	BaseModel     string `json:"base_model"`
	HFPath        string `json:"hf_path"`
	DefaultBranch string `json:"default_branch"`
	Metadata      struct {
		ModelParams  float64 `json:"model_params"`
		Architecture string  `json:"architecture"`
	} `json:"metadata"`
}

func (csghubAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	limit, page := c.Limit(req), c.page(req)
	q := url.Values{"sort": {sort.Key}, "per": {strconv.Itoa(limit)}, "page": {strconv.Itoa(page)}}
	if query := strings.TrimSpace(req.GetQuery()); query != "" {
		q.Set("search", query)
	}
	for facet, param := range csgFacetParams {
		if v := FilterOne(req, facet); v != "" {
			q.Set(param, v)
		}
	}
	var body struct {
		Total uint64     `json:"total"`
		Data  []csgModel `json:"data"`
	}
	if _, err := c.JSON(ctx, c.URL("api", "v1", "models"), q, &body); err != nil {
		return nil, err
	}
	resp := &v1.SearchResponse{Hits: make([]*v1.SearchHit, 0, len(body.Data)), Total: body.Total}
	for _, m := range body.Data {
		if hit := csgHit(c, m); hit != nil {
			resp.Hits = append(resp.Hits, hit)
		}
	}
	c.nextPage(resp, page, limit, body.Total)
	return resp, nil
}

func csgHit(c *Client, m csgModel) *v1.SearchHit {
	if m.Path == "" {
		return nil
	}
	author, name := splitRepo(m.Path)
	if m.Name != "" {
		name = m.Name
	}
	if m.Nickname != "" {
		name = m.Nickname
	}
	hit := newHit(m.Path, name, author)
	hit.Description, hit.License, hit.Private = Summary(m.Description), m.License, m.Private
	hit.Url = csgPage(c, m.Path)
	hit.CreatedAt, hit.UpdatedAt = Stamp(m.CreatedAt), Stamp(m.UpdatedAt)
	if m.Downloads > 0 {
		hit.Downloads = uint64(m.Downloads)
	}
	if m.Likes > 0 {
		hit.Likes = uint64(m.Likes)
	}
	for _, t := range m.Tags {
		if t.Name == "" {
			continue
		}
		hit.Tags = append(hit.Tags, t.Name)
		switch t.Category {
		case "task":
			if hit.Task == "" {
				hit.Task = t.Name
			}
		case "framework":
			if hit.Library == "" {
				hit.Library = t.Name
			}
		}
	}
	if m.Metadata.ModelParams > 0 {
		hit.Parameters = csgParameters(m.Metadata.ModelParams)
	}
	if m.Metadata.Architecture != "" {
		hit.Extra["architecture"] = m.Metadata.Architecture
	}
	if m.HFPath != "" {
		hit.Extra["hf_path"] = m.HFPath
	}
	if m.BaseModel != "" {
		hit.Extra["base_model"] = m.BaseModel
	}
	return hit
}

// The model page on the site people browse
func csgPage(c *Client, repo string) string { return c.Page("models", repo) }

// The hub reports model_params in billions, a large value is already a count
func csgParameters(v float64) uint64 {
	if v < 1e6 {
		v *= 1e9
	}
	return uint64(v + 0.5)
}

type csgBranch struct {
	Name   string `json:"name"`
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
}

type csgTree struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Size      uint64 `json:"size"`
	Path      string `json:"path"`
	Lfs       bool   `json:"lfs"`
	LfsSha256 string `json:"lfs_sha256"`
}

func csgBranches(ctx context.Context, c *Client, repo string) ([]csgBranch, error) {
	return csgData[[]csgBranch](ctx, c, c.URL("api", "v1", "models", repo, "branches"), nil)
}

// Reads the default branch from the repo detail, main when the hub does not say
func csgDefaultBranch(ctx context.Context, c *Client, repo string) string {
	m, err := csgData[csgModel](ctx, c, c.URL("api", "v1", "models", repo), nil)
	if err != nil || m.DefaultBranch == "" {
		return csgRevision
	}
	return m.DefaultBranch
}

func (csghubAPI) Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error) {
	if revision == "" {
		revision = csgDefaultBranch(ctx, c, repo)
	}
	branches, err := csgBranches(ctx, c, repo)
	if err != nil {
		return nil, err
	}
	model := &v1.Model{Repo: repo, Revision: revision}
	for _, b := range branches {
		if b.Name == revision {
			model.Commit = b.Commit.ID
			break
		}
	}
	if err := csgWalk(ctx, c, repo, revision, model); err != nil {
		return nil, err
	}
	return model, nil
}

// Lists the tree of a revision, descending into subdirectories to a fixed depth
func csgWalk(ctx context.Context, c *Client, repo, revision string, model *v1.Model) error {
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		q := url.Values{"ref": {revision}}
		if dir != "" {
			q.Set("path", dir)
		}
		entries, err := csgData[[]csgTree](ctx, c, c.URL("api", "v1", "models", repo, "tree"), q)
		if err != nil {
			return err
		}
		for _, e := range entries {
			path := e.Path
			if path == "" {
				path = strings.TrimPrefix(dir+"/"+e.Name, "/")
			}
			switch e.Type {
			case "file":
				a := &v1.Artifact{Path: path, SizeBytes: e.Size}
				if e.Lfs {
					a.Sha256 = Hex(e.LfsSha256)
				}
				model.Artifacts = append(model.Artifacts, a)
			case "dir", "directory":
				if depth < csgMaxDepth {
					if err := walk(path, depth+1); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	return walk("", 0)
}

func (csghubAPI) Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error) {
	branches, err := csgBranches(ctx, c, repo)
	if err != nil {
		return nil, err
	}
	def := csgDefaultBranch(ctx, c, repo)
	out := make([]*v1.Revision, 0, len(branches))
	for _, b := range branches {
		out = append(out, refRevision(b.Name, b.Commit.ID, b.Name == def, false))
	}
	return out, nil
}

func (csghubAPI) Card(ctx context.Context, c *Client, repo, revision string) (*v1.ModelCard, error) {
	var q url.Values
	if revision != "" {
		q = url.Values{"ref": {revision}}
	}
	card, err := c.CardText(ctx, c.URL("api", "v1", "models", repo, "raw", "README.md"), q, csgPage(c, repo))
	if err != nil || card.Markdown == "" {
		return card, err
	}
	// The raw file arrives inside the hub's JSON envelope
	var body csgEnvelope[string]
	if err := json.Unmarshal([]byte(card.Markdown), &body); err != nil {
		return nil, fmt.Errorf("decode %s card: %w", repo, err)
	}
	card.Markdown = body.Data
	return card, nil
}

func (csghubAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	revision := model.GetRevision()
	if revision == "" {
		revision = csgRevision
	}
	return c.Range(ctx, c.URL("hf", model.GetRepo(), "resolve", revision, artifact.GetPath()), artifact)
}
