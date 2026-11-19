package sources

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
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
		{Kind: TransportHFCLI, Name: "cli", Fields: map[string]string{"command": ""}, Inherit: []string{"endpoint", "token"}},
	},
	Description:   "Hugging Face Hub, and any hub that speaks its API",
	RepoExample:   "org/model",
	RepoPattern:   `^[\w.-]+/[\w.-]+$`,
	RevisionLabel: "revision",
	Sorts:         []string{SortTrending, SortDownloads, SortLikes, hubSortLikesWeek, hubSortLikesMonth, SortUpdated, SortCreated, SortParameters},
	SortKeys: map[string]string{
		SortTrending:      "trendingScore",
		SortDownloads:     "downloads",
		SortLikes:         "likes",
		hubSortLikesWeek:  "likes7d",
		hubSortLikesMonth: "likes30d",
		SortUpdated:       "lastModified",
		SortCreated:       "createdAt",
		SortParameters:    "num_parameters",
	},
	SortLabels: map[string]string{
		hubSortLikesWeek:  "Most liked this week",
		hubSortLikesMonth: "Most liked this month",
	},
	// The hub answers 400 to an ascending order of any other sort
	Reversible: []string{SortCreated, SortParameters},
	// Filters the hub takes whatever its taxonomy holds, after the facets read from it
	Facets: []*v1.Facet{
		NewFacet(hubFacetGated, "Access", false, Value("false", "Open"), Value("true", "Gated")),
		Hint(NewFacet(hubFacetInference, "Inference", false, Value("warm", ""), Value("cold", ""), Value("frozen", "")), "Whether the hub's own inference has the model loaded, can load it, or cannot serve it"),
		NewFacet(hubFacetRegion, "Region", false, Value("us", "US"), Value("eu", "EU")),
		Hint(Between(hubFacetParameters, "Parameters", ParameterSpans()...), "Parameter count, such as 1B to 8B"),
		Freeform(FacetAuthor, "Author"),
		Hint(Multi(Freeform(FacetTag, "Tag")), "Models carrying every tag named"),
		Hint(Freeform(FacetBaseModel, "Base model"), "Finetunes, quantizations, adapters, and merges of a repository"),
		Hint(Freeform(hubFacetDataset, "Trained on"), "Models trained on a dataset"),
	},
	Noise: []string{"endpoints_compatible", "eval-results", "autotrain_compatible", "text-generation-inference", "custom_code", "model-index", "has_space", "safetensors", "gguf", "pytorch", "transformers", ".+:.+", "[a-z]{2,3}"},
	Columns: slices.Concat(
		Shown(
			Described(Col(ColDownloads), "Downloads in the last 30 days"),
			Col(ColLikes),
			Col(ColParameters),
			Described(Col(ColSize), "The GGUF files together, the only size the hub lists"),
			Col(ColUpdated),
		),
		Cols(ColCreated, ColTask, ColLibrary, ColLicense, ColAuthor, ColGated),
		[]*v1.HitColumn{
			ExtraCol("downloads_all_time", "All-time downloads", AsCount, "Downloads since the repository was created, where Downloads counts the last 30 days"),
			ExtraCol("trending", "Trending", AsCount, "The hub's trending score"),
			ExtraCol("base_model", "Base model", AsList, "Repositories the model derives from"),
			ExtraCol("architecture", "Architecture", AsText, "Model architecture the GGUF header names"),
			ExtraCol("context", "Context", AsCount, "Context length in tokens the GGUF header declares"),
			ExtraCol("dtypes", "Tensor types", AsList, "Tensor data types of the safetensors weights, largest share first"),
		},
	),
	API: hubAPI{},
}

func init() { register(huggingface) }

// Sorts only the hub offers
const (
	hubSortLikesWeek  = "likes_7d"
	hubSortLikesMonth = "likes_30d"
)

// Facets only the hub offers
const (
	hubFacetLanguage   = "language"
	hubFacetOther      = "other"
	hubFacetDeploy     = "deploy"
	hubFacetRegion     = "region"
	hubFacetApp        = "app"
	hubFacetProvider   = "inference_provider"
	hubFacetInference  = "inference"
	hubFacetGated      = "gated"
	hubFacetParameters = "parameters"
	hubFacetDataset    = "dataset"
)

var hubExpand = []string{"pipeline_tag", "library_name", "gated", "private", "downloads", "downloadsAllTime", "likes", "lastModified", "createdAt", "tags", "safetensors", "gguf", "trendingScore"}

// A facet filled from one tag type of the hub's taxonomy
type hubTagType struct {
	kind, id, label string
	// What the hub's ids carry that the facet's values drop
	prefix string
	// Most used values kept, the hub listing them first. Zero keeps all.
	limit int
	hint  string
}

// Languages offered to pick from. The hub lists thousands, so the rest are reached through the tag facet.
const hubLanguages = 200

// Tag types the hub publishes, by the facet each fills. Its bucket type names storage, not
// models, and its datasets run to thousands in no order, so those are typed instead.
var hubTaxonomy = []hubTagType{
	{kind: "pipeline_tag", id: FacetTask, label: "Task"},
	{kind: "library", id: FacetLibrary, label: "Library"},
	{kind: "license", id: FacetLicense, label: "License", prefix: "license:"},
	{kind: "language", id: hubFacetLanguage, label: "Language", limit: hubLanguages, hint: "The most used languages. Name any other by its code as a tag."},
	{kind: "other", id: hubFacetOther, label: "Other"},
	{kind: "deploy", id: hubFacetDeploy, label: "Deployment", hint: "Services the hub deploys the model to"},
}

// Tag prefixes of the facets the hub filters as tags, a bare tag when blank
var hubTagFacets = []struct{ id, prefix string }{
	{FacetLicense, "license:"},
	{hubFacetLanguage, ""},
	{hubFacetOther, ""},
	{hubFacetDeploy, ""},
	{hubFacetRegion, "region:"},
	{FacetBaseModel, "base_model:"},
	{hubFacetDataset, "dataset:"},
	{FacetTag, ""},
}

// Inference providers and local apps the hub filtered by on 2026-11-18, kept for when it names none
var (
	hubProviders = []string{"baseten", "cerebras", "cohere", "deepinfra", "fal-ai", "featherless-ai", "fireworks-ai", "groq", "hf-inference", "novita", "nscale", "openrouter", "ovhcloud", "publicai", "replicate", "scaleway", "together", "wavespeed", "zai-org"}
	hubApps      = []string{"llama.cpp", "llama-app", "lmstudio", "jan", "drawthings", "diffusionbee", "joyfusion", "vllm", "ollama", "mlx-lm", "docker-model-runner", "lemonade", "sglang", "unsloth", "pi", "hermes-agent", "atomic-chat", "openclaw"}
)

// Names of the providers and apps whose ids do not read as one
var hubNames = map[string]string{
	"deepinfra":           "DeepInfra",
	"fal-ai":              "fal",
	"featherless-ai":      "Featherless AI",
	"fireworks-ai":        "Fireworks AI",
	"hf-inference":        "HF Inference",
	"openrouter":          "OpenRouter",
	"ovhcloud":            "OVHcloud",
	"publicai":            "Public AI",
	"together":            "Together AI",
	"wavespeed":           "WaveSpeed",
	"zai-org":             "Z.ai",
	"llama.cpp":           "llama.cpp",
	"lmstudio":            "LM Studio",
	"drawthings":          "Draw Things",
	"diffusionbee":        "DiffusionBee",
	"joyfusion":           "JoyFusion",
	"vllm":                "vLLM",
	"mlx-lm":              "MLX LM",
	"docker-model-runner": "Docker Model Runner",
	"sglang":              "SGLang",
	"openclaw":            "OpenClaw",
}

// The Hub API: a Link paged model list, a tree with LFS digests, and refs
type hubAPI struct{}

type hubTag struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	SubType string `json:"subType"`
}

// Loads the facets the hub's tag taxonomy fills, then the apps and inference providers it filters by.
func (hubAPI) Facets(ctx context.Context, c *Client) ([]*v1.Facet, error) {
	var tags map[string][]hubTag
	if _, err := c.JSON(ctx, c.URL("api", "models-tags-by-type"), nil, &tags); err != nil {
		return nil, err
	}
	var out []*v1.Facet
	for _, t := range hubTaxonomy {
		entries := tags[t.kind]
		if t.limit > 0 && len(entries) > t.limit {
			entries = entries[:t.limit]
		}
		values := make([]*v1.FacetValue, 0, len(entries))
		for _, e := range entries {
			values = append(values, GroupedValue(strings.TrimPrefix(e.ID, t.prefix), e.Label, e.SubType))
		}
		out = append(out, Hint(NewFacet(t.id, t.label, false, values...), t.hint))
	}
	return append(out,
		Hint(NewFacet(hubFacetApp, "App", true, hubOptions(ctx, c, "apps", hubApps)...), "Local apps that run the model"),
		Hint(NewFacet(hubFacetProvider, "Provider", true, hubOptions(ctx, c, "inference_provider", hubProviders)...), "Inference providers serving the model through the hub"),
	), nil
}

var hubOption = regexp.MustCompile(`\\"([^"\\]+)\\"`)

// The values the hub takes for a list parameter. It publishes no list of them, so they are read
// from the error it answers an unknown value with, the known ones standing in when that names none.
func hubOptions(ctx context.Context, c *Client, param string, known []string) []*v1.FacetValue {
	ids := known
	var se *StatusError
	if _, err := c.JSON(ctx, c.URL("api", "models"), url.Values{"limit": {"1"}, param: {"-"}}, nil); errors.As(err, &se) && se.Code == http.StatusBadRequest {
		var named []string
		for _, m := range hubOption.FindAllStringSubmatch(se.Body, -1) {
			named = append(named, m[1])
		}
		if len(named) > 0 {
			ids = named
		}
	}
	values := make([]*v1.FacetValue, 0, len(ids))
	for _, id := range ids {
		values = append(values, Value(id, hubNames[id]))
	}
	return values
}

type hubItem struct {
	ID            string          `json:"id"`
	Sha           string          `json:"sha"`
	Author        string          `json:"author"`
	Downloads     uint64          `json:"downloads"`
	AllDownloads  uint64          `json:"downloadsAllTime"`
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
		// Parameter counts by tensor dtype, such as BF16
		Parameters map[string]uint64 `json:"parameters"`
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
		"direction": {direction(sort.Ascending, "1", "-1")},
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
	if err := hubFilters(req, q); err != nil {
		return nil, err
	}
	for _, t := range req.GetTags() {
		q.Add("filter", t)
	}
	for _, f := range hubTagFacets {
		for _, v := range Filter(req, f.id) {
			q.Add("filter", f.prefix+strings.TrimPrefix(v, f.prefix))
		}
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

// Adds the filters the hub takes as parameters of their own rather than as tags.
func hubFilters(req *v1.SearchRequest, q url.Values) error {
	// The hub drops a gated value it cannot read and answers unfiltered, so it is checked here
	if raw := FilterOne(req, hubFacetGated); raw != "" {
		gated, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("%w: %s %q is neither true nor false", ErrSource, hubFacetGated, raw)
		}
		q.Set("gated", strconv.FormatBool(gated))
	}
	if status := FilterOne(req, hubFacetInference); status != "" {
		q.Set("inference", status)
	}
	if apps := Filter(req, hubFacetApp); len(apps) > 0 {
		q.Set("apps", strings.Join(apps, ","))
	}
	if providers := Filter(req, hubFacetProvider); len(providers) > 0 {
		q.Set("inference_provider", strings.Join(providers, ","))
	}
	// Counts go whole, since the hub reads a suffix other than B as part of the number
	bounds, err := Range(req, hubFacetParameters)
	if err != nil {
		return err
	}
	var sides []string
	if bounds.Min != nil {
		sides = append(sides, "min:"+strconv.FormatUint(*bounds.Min, 10))
	}
	if bounds.Max != nil {
		sides = append(sides, "max:"+strconv.FormatUint(*bounds.Max, 10))
	}
	if len(sides) > 0 {
		q.Set("num_parameters", strings.Join(sides, ","))
	}
	return nil
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
	var bases []string
	for _, t := range it.Tags {
		if license, ok := strings.CutPrefix(t, "license:"); ok {
			hit.License = license
		}
		// A base is tagged bare and once more under its relation, as base_model:quantized:org/name
		if base, ok := strings.CutPrefix(t, "base_model:"); ok && !strings.Contains(base, ":") {
			bases = append(bases, base)
		}
	}
	if len(bases) > 0 {
		hit.Extra["base_model"] = strings.Join(bases, ",")
	}
	if it.AllDownloads > 0 {
		hit.Extra["downloads_all_time"] = strconv.FormatUint(it.AllDownloads, 10)
	}
	if it.Safetensors != nil {
		if it.Safetensors.Total > 0 {
			hit.Parameters = it.Safetensors.Total
		}
		// Dtypes largest share first, as the hub lists tensor types
		dtypes := slices.SortedFunc(maps.Keys(it.Safetensors.Parameters), func(a, b string) int {
			return cmp.Or(cmp.Compare(it.Safetensors.Parameters[b], it.Safetensors.Parameters[a]), strings.Compare(a, b))
		})
		if len(dtypes) > 0 {
			hit.Extra["dtypes"] = strings.Join(dtypes, ",")
		}
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

// How long one hub record answers both the resolve and the card of a page view
const hubDetailTTL = time.Minute

// The hub's record of a repository at a revision, the default one when omitted, with the commit it names
func hubGet(ctx context.Context, c *Client, repo, revision string) (hubItem, error) {
	memo := Cached(c, "hub-model:"+repo+"@"+revision, func() *Memo[hubItem] { return &Memo[hubItem]{TTL: hubDetailTTL} })
	return memo.Get(ctx, func(ctx context.Context) (hubItem, error) {
		var it hubItem
		segments := []string{"api", "models", repo}
		if revision != "" {
			segments = append(segments, "revision", revision)
		}
		if _, err := c.JSON(ctx, c.URL(segments...), nil, &it); err != nil {
			return hubItem{}, err
		}
		if it.Sha == "" {
			return hubItem{}, fmt.Errorf("%s: the hub names no commit at %s", repo, firstOr(revision, "its default revision"))
		}
		return it, nil
	})
}

// Resolves the revision commit, using the Hub default when omitted.
func hubSha(ctx context.Context, c *Client, repo, revision string) (string, error) {
	it, err := hubGet(ctx, c, repo, revision)
	if err != nil {
		return "", err
	}
	return it.Sha, nil
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

// The README at the revision, the default branch when none is named, with the hub's listing of the repository
func (hubAPI) Card(ctx context.Context, c *Client, repo, revision string) (*v1.ModelCard, error) {
	it, err := hubGet(ctx, c, repo, revision)
	if err != nil {
		return nil, err
	}
	if revision == "" {
		if revision, _, err = hubDefault(ctx, c, repo); err != nil {
			return nil, err
		}
	}
	card, err := c.CardText(ctx, c.URL(repo, "raw", revision, "README.md"), nil, hubPage(c, repo))
	if err != nil {
		return nil, err
	}
	card.Hit = hubHit(c, it)
	return card, nil
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
