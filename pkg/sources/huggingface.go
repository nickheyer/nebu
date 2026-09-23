package sources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The Hugging Face Hub
var huggingface = &Catalog{
	ID:   "huggingface",
	Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE,
	Name: "Hugging Face",
	Transports: []Use{
		httpUse("https://huggingface.co", "HF_TOKEN"),
		{Kind: TransportHFCLI, Name: "cli", Fields: map[string]string{"command": ""}, Inherit: []string{"endpoint", "token_env"}},
	},
	Description:   "Hugging Face Hub, and any hub that speaks its API",
	RepoExample:   "org/model",
	RepoPattern:   `^[\w.-]+/[\w.-]+$`,
	RevisionLabel: "revision",
	Sorts:         []string{SortTrending, SortDownloads, SortLikes, SortUpdated, SortCreated},
	// The API orders every one of these descending only
	SortKeys: map[string]string{
		SortTrending:  "trendingScore",
		SortDownloads: "downloads",
		SortLikes:     "likes",
		SortUpdated:   "lastModified",
		SortCreated:   "createdAt",
	},
	Noise: []string{"endpoints_compatible", "eval-results", "autotrain_compatible", "text-generation-inference", "custom_code", "model-index", "has_space", "safetensors", "gguf", "pytorch", "transformers", ".+:.+", "[a-z]{2,3}"},
	HitFields: []*v1.ConfigField{
		{Name: "architecture", Label: "Architecture", Description: "Model architecture the GGUF header names"},
		{Name: "context", Label: "Context", Description: "Context length in tokens the GGUF header declares"},
	},
	API: hubAPI{},
}

func init() { register(huggingface) }

var hubExpand = []string{"pipeline_tag", "library_name", "gated", "private", "downloads", "likes", "lastModified", "createdAt", "tags", "safetensors", "gguf", "trendingScore"}

// Tag types the hub publishes, by the facet each fills
var hubTaxonomy = []taxonomy{{"pipeline_tag", FacetTask, "Task"}, {"library", FacetLibrary, "Library"}, {"license", FacetLicense, "License"}}

// The Hub API: a Link paged model list, a tree with LFS digests, and refs
type hubAPI struct{}

type hubTag struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	SubType string `json:"subType"`
}

// Loads facets from the Hub tag taxonomy.
func (hubAPI) Facets(ctx context.Context, c *Client) ([]*v1.Facet, error) {
	out, err := taxonomyFacets(ctx, hubTaxonomy, func(ctx context.Context, kind string) ([]*v1.FacetValue, error) {
		var body map[string][]hubTag
		if _, err := c.JSON(ctx, c.URL("api", "models-tags-by-type"), url.Values{"type": {kind}}, &body); err != nil {
			return nil, err
		}
		var values []*v1.FacetValue
		for _, e := range body[kind] {
			values = append(values, GroupedValue(strings.TrimPrefix(e.ID, kind+":"), e.Label, e.SubType))
		}
		return values, nil
	})
	if err != nil {
		return nil, err
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

// Searches the hub. Several formats become one request per tag merged by the sort, since the
// hub joins repeated tag filters with and.
func (hubAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	tags := hubFormatTags(Filter(req, FacetFormat))
	if len(tags) > 1 {
		return hubSearchAny(ctx, c, req, sort, tags)
	}
	tag := ""
	if len(tags) == 1 {
		tag = tags[0]
	}
	return hubSearch(ctx, c, req, sort, tag, strings.TrimSpace(req.GetCursor()))
}

// Fetches one page of the model list, filtered to one format tag when given.
func hubSearch(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort, tag, cursor string) (*v1.SearchResponse, error) {
	q := url.Values{
		"limit":     {strconv.Itoa(c.Limit(req))},
		"sort":      {sort.Key},
		"direction": {"-1"},
	}
	if query := strings.TrimSpace(req.GetQuery()); query != "" {
		q.Set("search", query)
	}
	if author := Author(req); author != "" {
		q.Set("author", author)
	}
	if cursor != "" {
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
	if tag != "" {
		q.Add("filter", tag)
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

// Marks a cursor that pages several tag streams at once.
const hubUnionCursor = "union:"

// Fetches a page per format tag in parallel and merges them by the sort, dropping repositories
// listed under several tags. The cursor carries every stream's position. The union's size is
// unknown, so the total is left unset.
func hubSearchAny(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort, tags []string) (*v1.SearchResponse, error) {
	cursors := map[string]string{}
	if cursor := strings.TrimSpace(req.GetCursor()); cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, hubUnionCursor))
		if !strings.HasPrefix(cursor, hubUnionCursor) || err != nil || json.Unmarshal(data, &cursors) != nil {
			return nil, fmt.Errorf("%w: cursor %q is not one this search issued", ErrSource, cursor)
		}
	} else {
		for _, tag := range tags {
			cursors[tag] = ""
		}
	}
	type page struct {
		tag  string
		resp *v1.SearchResponse
		err  error
	}
	// Streams absent from the cursor are exhausted.
	var pages []page
	for _, tag := range tags {
		if _, ok := cursors[tag]; ok {
			pages = append(pages, page{tag: tag})
		}
	}
	var wg sync.WaitGroup
	for i := range pages {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pages[i].resp, pages[i].err = hubSearch(ctx, c, req, sort, pages[i].tag, cursors[pages[i].tag])
		}()
	}
	wg.Wait()
	out := &v1.SearchResponse{}
	next := map[string]string{}
	seen := map[string]bool{}
	for _, p := range pages {
		if p.err != nil {
			return nil, fmt.Errorf("%s: %w", p.tag, p.err)
		}
		if p.resp.GetNextCursor() != "" {
			next[p.tag] = p.resp.GetNextCursor()
		}
	}
	for i := 0; ; i++ {
		added := false
		for _, p := range pages {
			if i < len(p.resp.GetHits()) {
				added = true
				if h := p.resp.GetHits()[i]; !seen[h.GetRepo()] {
					seen[h.GetRepo()] = true
					out.Hits = append(out.Hits, h)
				}
			}
		}
		if !added {
			break
		}
	}
	SortHits(out.Hits, sort.ID, sort.Ascending)
	if len(next) > 0 {
		data, err := json.Marshal(next)
		if err != nil {
			return nil, err
		}
		out.NextCursor = hubUnionCursor + base64.RawURLEncoding.EncodeToString(data)
	}
	return out, nil
}

// Maps format IDs to Hub tags. Standalone diffusion checkpoints use safetensors tags.
func hubFormatTags(formats []string) []string {
	var out []string
	for _, f := range formats {
		tag := f
		switch f {
		case "diffusion":
			tag = "safetensors"
		case "nemo", "nemo2":
			tag = "nemo"
		}
		if !slices.Contains(out, tag) {
			out = append(out, tag)
		}
	}
	return out
}

func hubHit(c *Client, it hubItem) *v1.SearchHit {
	author, name := splitRepo(it.ID)
	if it.Author != "" {
		author = it.Author
	}
	hit := newHit(it.ID, name, author)
	hit.Downloads, hit.Likes, hit.Tags = it.Downloads, it.Likes, it.Tags
	hit.Task, hit.Library = it.PipelineTag, it.LibraryName
	hit.Private, hit.Gated = it.Private, hubGated(it.Gated)
	hit.Url = hubPage(c, it.ID)
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

// The model page on the hub
func hubPage(c *Client, repo string) string { return c.Page(repo) }

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

// Resolves the revision commit, using the Hub default when omitted.
func hubSha(ctx context.Context, c *Client, repo, revision string) (string, error) {
	var info struct {
		Sha string `json:"sha"`
	}
	segments := []string{"api", "models", repo}
	if revision != "" {
		segments = append(segments, "revision", revision)
	}
	if _, err := c.JSON(ctx, c.URL(segments...), nil, &info); err != nil {
		return "", err
	}
	if info.Sha == "" {
		return "", fmt.Errorf("%s: the hub names no commit at %s", repo, firstOr(revision, "its default revision"))
	}
	return info.Sha, nil
}

// Resolves the Hub default branch, falling back to its commit SHA.
func hubDefault(ctx context.Context, c *Client, repo string) (revision, sha string, err error) {
	if sha, err = hubSha(ctx, c, repo, ""); err != nil {
		return "", "", err
	}
	refs, err := hubRefs(ctx, c, repo)
	if err != nil {
		return "", "", err
	}
	for _, b := range refs.Branches {
		if b.TargetCommit == sha {
			return b.Name, sha, nil
		}
	}
	return sha, sha, nil
}

func (hubAPI) Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error) {
	var sha string
	var err error
	if revision == "" {
		revision, sha, err = hubDefault(ctx, c, repo)
	} else {
		sha, err = hubSha(ctx, c, repo, revision)
	}
	if err != nil {
		return nil, err
	}
	model := &v1.Model{Repo: repo, Revision: revision, Commit: sha}
	err = eachPage(ctx, c, c.URL("api", "models", repo, "tree", revision), url.Values{"recursive": {"true"}}, func(entries []hubTree) {
		for _, e := range entries {
			if e.Type != "file" {
				continue
			}
			a := &v1.Artifact{Path: e.Path, SizeBytes: e.Size, Url: c.URL(repo, "resolve", revision, e.Path)}
			if e.Lfs != nil {
				a.Sha256 = e.Lfs.Oid
				a.SizeBytes = e.Lfs.Size
			}
			model.Artifacts = append(model.Artifacts, a)
		}
	})
	if err != nil {
		return nil, err
	}
	return model, nil
}

type hubRef struct {
	Name         string `json:"name"`
	Ref          string `json:"ref"`
	TargetCommit string `json:"targetCommit"`
}

// The branches and tags of a repo as the hub lists them
type hubRefList struct {
	Branches []hubRef `json:"branches"`
	Tags     []hubRef `json:"tags"`
}

func hubRefs(ctx context.Context, c *Client, repo string) (*hubRefList, error) {
	var body hubRefList
	if _, err := c.JSON(ctx, c.URL("api", "models", repo, "refs"), nil, &body); err != nil {
		return nil, err
	}
	return &body, nil
}

// Every branch and tag, the branch at the hub's default commit marked as the default
func (hubAPI) Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error) {
	sha, err := hubSha(ctx, c, repo, "")
	if err != nil {
		return nil, err
	}
	refs, err := hubRefs(ctx, c, repo)
	if err != nil {
		return nil, err
	}
	var out []*v1.Revision
	for _, b := range refs.Branches {
		out = append(out, refRevision(b.Name, b.TargetCommit, b.TargetCommit == sha, false))
	}
	for _, t := range refs.Tags {
		out = append(out, refRevision(t.Name, t.TargetCommit, false, true))
	}
	return out, nil
}

func (hubAPI) Card(ctx context.Context, c *Client, repo, revision string) (*v1.ModelCard, error) {
	if revision == "" {
		var err error
		if revision, _, err = hubDefault(ctx, c, repo); err != nil {
			return nil, err
		}
	}
	return c.CardText(ctx, c.URL(repo, "raw", revision, "README.md"), nil, hubPage(c, repo))
}

// Opens a file for ranged reads over HTTP, or whole through the CLI when on
func (hubAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	b, err := c.Range(ctx, c.URL(model.GetRepo(), "resolve", model.GetRevision(), artifact.GetPath()), artifact)
	if err != nil {
		return nil, err
	}
	cli := c.CLI()
	if cli == nil {
		return b, nil
	}
	whole, err := cli.Open(ctx, model.GetRepo()+"@"+model.GetRevision()+"/"+artifact.GetPath(), int64(artifact.GetSizeBytes()))
	if err != nil {
		return nil, err
	}
	return &rangedWhole{Blob: b, whole: whole}, nil
}
