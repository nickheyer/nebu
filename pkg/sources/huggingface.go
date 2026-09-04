package sources

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The Hugging Face Hub
var huggingface = &Catalog{
	ID:            "huggingface",
	Kind:          v1.SourceKind_SOURCE_KIND_HUGGINGFACE,
	Endpoint:      "https://huggingface.co",
	TokenEnv:      "HF_TOKEN",
	Description:   "Hugging Face Hub",
	RepoExample:   "org/model",
	RepoPattern:   `^[\w.-]+/[\w.-]+$`,
	RevisionLabel: "revision",
	Sorts:         []string{SortTrending, SortDownloads, SortLikes, SortUpdated, SortCreated},
	API:           hubAPI{},
}

func init() { register(huggingface) }

const hubRevision = "main"

// Hub sort keys by shared sort id, the API orders every one of them descending only
var hubSortKeys = map[string]string{
	SortTrending:  "trendingScore",
	SortDownloads: "downloads",
	SortLikes:     "likes",
	SortUpdated:   "lastModified",
	SortCreated:   "createdAt",
}

var hubExpand = []string{"pipeline_tag", "library_name", "gated", "private", "downloads", "likes", "lastModified", "createdAt", "tags", "safetensors", "gguf", "trendingScore"}

// The Hub API: a model list paged by a Link cursor, a tree with LFS digests, refs, and raw files
type hubAPI struct{}

type hubTag struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	SubType string `json:"subType"`
}

// Reads the hub's own tag taxonomy so the facets never go stale
func (hubAPI) Facets(ctx context.Context, c *Client) ([]*v1.Facet, error) {
	var out []*v1.Facet
	for _, t := range []struct{ kind, id, label string }{
		{"pipeline_tag", FacetTask, "Task"},
		{"library", FacetLibrary, "Library"},
		{"license", FacetLicense, "License"},
	} {
		var body map[string][]hubTag
		if _, err := c.JSON(ctx, c.URL("api", "models-tags-by-type"), url.Values{"type": {t.kind}}, &body); err != nil {
			return nil, err
		}
		facet := NewFacet(t.id, t.label, false)
		for _, e := range body[t.kind] {
			facet.Values = append(facet.Values, GroupedValue(strings.TrimPrefix(e.ID, t.kind+":"), e.Label, e.SubType))
		}
		out = append(out, facet)
	}
	return append(out, Freeform(FacetAuthor, "Author")), nil
}

type hubItem struct {
	ID            string          `json:"id"`
	Author        string          `json:"author"`
	Downloads     uint64          `json:"downloads"`
	Likes         uint64          `json:"likes"`
	LastModified  time.Time       `json:"lastModified"`
	CreatedAt     time.Time       `json:"createdAt"`
	Tags          []string        `json:"tags"`
	PipelineTag   string          `json:"pipeline_tag"`
	LibraryName   string          `json:"library_name"`
	Gated         json.RawMessage `json:"gated"`
	Private       bool            `json:"private"`
	TrendingScore float64         `json:"trendingScore"`
	Safetensors   *struct {
		Total uint64 `json:"total"`
	} `json:"safetensors"`
	GGUF *struct {
		Total         uint64 `json:"total"`
		Architecture  string `json:"architecture"`
		ContextLength uint64 `json:"context_length"`
		TotalFileSize uint64 `json:"totalFileSize"`
	} `json:"gguf"`
}

var hubCursor = regexp.MustCompile(`[?&]cursor=([^&>]+)`)

func (hubAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	q := url.Values{
		"limit":     {strconv.Itoa(c.Limit(req))},
		"sort":      {hubSortKeys[sort.ID]},
		"direction": {"-1"},
	}
	if query := strings.TrimSpace(req.GetQuery()); query != "" {
		q.Set("search", query)
	}
	if author := Author(req); author != "" {
		q.Set("author", author)
	}
	if cursor := strings.TrimSpace(req.GetCursor()); cursor != "" {
		q.Set("cursor", cursor)
	}
	if task := FilterOne(req, FacetTask); task != "" {
		q.Set("pipeline_tag", task)
	}
	if lib := FilterOne(req, FacetLibrary); lib != "" {
		q.Set("library", lib)
	}
	for _, license := range Filter(req, FacetLicense) {
		q.Add("filter", "license:"+strings.TrimPrefix(license, "license:"))
	}
	for _, t := range req.GetTags() {
		q.Add("filter", t)
	}
	for _, t := range Filter(req, FacetTag) {
		q.Add("filter", t)
	}
	for _, e := range hubExpand {
		q.Add("expand[]", e)
	}
	var items []hubItem
	header, err := c.JSON(ctx, c.URL("api", "models"), q, &items)
	if err != nil {
		return nil, err
	}
	resp := &v1.SearchResponse{Hits: make([]*v1.SearchHit, 0, len(items))}
	for _, it := range items {
		resp.Hits = append(resp.Hits, hubHit(c, it))
	}
	if m := hubCursor.FindStringSubmatch(header.Get("Link")); m != nil {
		if cursor, err := url.QueryUnescape(m[1]); err == nil {
			resp.NextCursor = cursor
		}
	}
	if total, err := strconv.ParseUint(header.Get("X-Total-Count"), 10, 64); err == nil {
		resp.Total = total
	}
	return resp, nil
}

func hubHit(c *Client, it hubItem) *v1.SearchHit {
	author, name, _ := strings.Cut(it.ID, "/")
	if it.Author != "" {
		author = it.Author
	}
	if name == "" {
		name = it.ID
	}
	hit := &v1.SearchHit{
		Repo:      it.ID,
		Name:      name,
		Author:    author,
		Downloads: it.Downloads,
		Likes:     it.Likes,
		Tags:      it.Tags,
		Task:      it.PipelineTag,
		Library:   it.LibraryName,
		Private:   it.Private,
		Gated:     hubGated(it.Gated),
		Url:       c.Base() + "/" + it.ID,
		Extra:     map[string]string{},
	}
	if !it.LastModified.IsZero() {
		hit.UpdatedAt = timestamppb.New(it.LastModified)
	}
	if !it.CreatedAt.IsZero() {
		hit.CreatedAt = timestamppb.New(it.CreatedAt)
	}
	for _, t := range it.Tags {
		if strings.HasPrefix(t, "license:") {
			hit.License = strings.TrimPrefix(t, "license:")
		}
	}
	if it.Safetensors != nil && it.Safetensors.Total > 0 {
		hit.Parameters = it.Safetensors.Total
	}
	if it.GGUF != nil {
		if it.GGUF.Total > 0 {
			hit.Parameters = it.GGUF.Total
		}
		hit.SizeBytes = it.GGUF.TotalFileSize
		if it.GGUF.Architecture != "" {
			hit.Extra["architecture"] = it.GGUF.Architecture
		}
		if it.GGUF.ContextLength > 0 {
			hit.Extra["context"] = strconv.FormatUint(it.GGUF.ContextLength, 10)
		}
	}
	if it.TrendingScore > 0 {
		hit.Extra["trending"] = strconv.FormatFloat(it.TrendingScore, 'f', 0, 64)
	}
	return hit
}

// The hub sends gated as false or as the mode, auto or manual
func hubGated(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "false" && s != "null"
}

type hubTree struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size uint64 `json:"size"`
	Lfs  *struct {
		Oid  string `json:"oid"`
		Size uint64 `json:"size"`
	} `json:"lfs"`
}

func (hubAPI) Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error) {
	if revision == "" {
		revision = hubRevision
	}
	var info struct {
		Sha string `json:"sha"`
	}
	if _, err := c.JSON(ctx, c.URL("api", "models", repo, "revision", revision), nil, &info); err != nil {
		return nil, err
	}
	model := &v1.Model{Repo: repo, Revision: revision, Commit: info.Sha}
	next := c.URL("api", "models", repo, "tree", revision)
	query := url.Values{"recursive": {"true"}}
	for next != "" {
		var entries []hubTree
		header, err := c.JSON(ctx, next, query, &entries)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Type != "file" {
				continue
			}
			a := &v1.Artifact{Path: e.Path, SizeBytes: e.Size}
			if e.Lfs != nil {
				a.Sha256 = e.Lfs.Oid
				a.SizeBytes = e.Lfs.Size
			}
			model.Artifacts = append(model.Artifacts, a)
		}
		next, query = NextLink(header.Get("Link"), c.Base()), nil
	}
	return model, nil
}

type hubRef struct {
	Name         string `json:"name"`
	Ref          string `json:"ref"`
	TargetCommit string `json:"targetCommit"`
}

func (hubAPI) Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error) {
	var body struct {
		Branches []hubRef `json:"branches"`
		Tags     []hubRef `json:"tags"`
	}
	if _, err := c.JSON(ctx, c.URL("api", "models", repo, "refs"), nil, &body); err != nil {
		return nil, err
	}
	var out []*v1.Revision
	add := func(r hubRef, detail string) {
		out = append(out, &v1.Revision{Name: r.Name, Commit: r.TargetCommit, Default: r.Name == hubRevision, Detail: detail})
	}
	for _, b := range body.Branches {
		add(b, "branch")
	}
	for _, t := range body.Tags {
		add(t, "tag")
	}
	return out, nil
}

func (hubAPI) Card(ctx context.Context, c *Client, repo, revision string) (*v1.ModelCard, error) {
	if revision == "" {
		revision = hubRevision
	}
	return c.CardText(ctx, c.URL(repo, "raw", revision, "README.md"), nil, c.Base()+"/"+repo)
}

func (hubAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	return c.Range(c.URL(model.GetRepo(), "resolve", model.GetRevision(), artifact.GetPath()), artifact)
}
