package sources

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Civitai, checkpoints and adapters for image and video models
var civitai = &Catalog{
	ID:            "civitai",
	Kind:          v1.SourceKind_SOURCE_KIND_CIVITAI,
	Name:          "Civitai",
	Transports:    []Use{httpUse("https://civitai.com", "CIVITAI_API_TOKEN")},
	WebPath:       "/models",
	AuthRequired:  true,
	Description:   "Civitai, checkpoints and adapters for image and video models",
	RepoExample:   "4201",
	RepoPattern:   `^\d+(/\d+)?$`,
	RevisionLabel: "version",
	Sorts:         []string{SortDownloads, SortLikes, SortTrending, SortCreated},
	Reversible:    []string{SortCreated},
	Facets:        civFacets(),
	HitFields: []*v1.ConfigField{
		{Name: "nsfw", Label: "NSFW", Type: v1.ConfigType_CONFIG_TYPE_BOOL, Description: "Marked adult content by the source"},
		{Name: "base_model", Label: "Base model", Description: "Base model this was made for"},
		{Name: "version", Label: "Version", Description: "Latest version"},
	},
	API: civitaiAPI{},
}

func init() { register(civitai) }

const civTrainingData = "Training Data"

// Civitai sort names by shared sort id
//
// Recently Added is not here because the API only accepts it inside a collection.
var civSortKeys = map[string]string{
	SortDownloads: "Most Downloaded",
	SortLikes:     "Most Liked",
	SortTrending:  "Highest Rated",
	SortCreated:   "Newest",
}

// Model types the API filters on
var civModelTypes = []string{"Checkpoint", "TextualInversion", "Hypernetwork", "AestheticGradient", "LORA", "LoCon", "DoRA", "Controlnet", "Upscaler", "MotionModule", "VAE", "TextEncoder", "UNet", "CLIPVision", "Poses", "Wildcards", "Workflows", "ComfyWorkflows", "Detection", "VisionLanguage", "CLIP", "LLM", "Other"}

// Periods the API ranks over
var civPeriods = []struct{ id, label string }{{"Day", "Day"}, {"Week", "Week"}, {"Month", "Month"}, {"Year", "Year"}, {"AllTime", "All time"}}

var civRepoRe = regexp.MustCompile(`^(\d+)(?:/(\d+))?$`)

// The Civitai API: models with versions as variants, cursor paging, and signed downloads
type civitaiAPI struct{}

// Fixed facets, the API takes these enums plus free text
func civFacets() []*v1.Facet {
	types := NewFacet(FacetType, "Type", true)
	for _, t := range civModelTypes {
		types.Values = append(types.Values, Value(t, t))
	}
	period := NewFacet(FacetPeriod, "Period", false)
	for _, p := range civPeriods {
		period.Values = append(period.Values, Value(p.id, p.label))
	}
	return []*v1.Facet{
		types,
		period,
		Freeform(FacetBaseModel, "Base model"),
		Freeform(FacetTag, "Tag"),
		Freeform(FacetAuthor, "Author"),
		NewFacet(FacetNSFW, "NSFW", false, Value("false", "Hidden"), Value("true", "Shown")),
	}
}

type civFile struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Primary     bool              `json:"primary"`
	SizeKB      float64           `json:"sizeKB"`
	Hashes      map[string]string `json:"hashes"`
	DownloadURL string            `json:"downloadUrl"`
	Metadata    struct {
		Format string `json:"format"`
		Size   string `json:"size"`
		FP     string `json:"fp"`
	} `json:"metadata"`
}

// Names every file of a version, telling apart files published under one name
//
// A version often carries a pruned fp16 and a full fp32 build with the same
// file name, so a colliding name gets its precision and size spliced in before
// the extension, and the file id when even that ties.
func civArtifactNames(files []civFile) map[int64]string {
	counts := map[string]int{}
	for _, f := range files {
		if f.Type != civTrainingData {
			counts[f.Name]++
		}
	}
	out := map[int64]string{}
	seen := map[string]bool{}
	for _, f := range files {
		if f.Type == civTrainingData {
			continue
		}
		name := f.Name
		if counts[f.Name] > 1 {
			var parts []string
			for _, p := range []string{f.Metadata.FP, f.Metadata.Size} {
				if p != "" {
					parts = append(parts, strings.ToLower(p))
				}
			}
			if len(parts) == 0 || seen[civWithSuffix(f.Name, strings.Join(parts, "-"))] {
				parts = append(parts, strconv.FormatInt(f.ID, 10))
			}
			name = civWithSuffix(f.Name, strings.Join(parts, "-"))
		}
		seen[name] = true
		out[f.ID] = name
	}
	return out
}

// Inserts a suffix before the file extension
func civWithSuffix(name, suffix string) string {
	ext := path.Ext(name)
	return strings.TrimSuffix(name, ext) + "." + suffix + ext
}

type civVersion struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	BaseModel   string    `json:"baseModel"`
	PublishedAt time.Time `json:"publishedAt"`
	Files       []civFile `json:"files"`
}

type civModel struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	NSFW        bool     `json:"nsfw"`
	Tags        []string `json:"tags"`
	Description string   `json:"description"`
	Stats       struct {
		DownloadCount uint64 `json:"downloadCount"`
		ThumbsUpCount uint64 `json:"thumbsUpCount"`
	} `json:"stats"`
	Creator struct {
		Username string `json:"username"`
	} `json:"creator"`
	Versions []civVersion `json:"modelVersions"`
}

func (civitaiAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	key := civSortKeys[sort.ID]
	// Only the creation order can be flipped, the API has no direction switch
	if sort.Ascending {
		key = "Oldest"
	}
	q := url.Values{
		"limit": {strconv.Itoa(c.Limit(req))},
		"sort":  {key},
		"nsfw":  {strconv.FormatBool(civShowNSFW(req))},
	}
	if query := strings.TrimSpace(req.GetQuery()); query != "" {
		q.Set("query", query)
	}
	if cursor := strings.TrimSpace(req.GetCursor()); cursor != "" {
		q.Set("cursor", cursor)
	}
	if author := Author(req); author != "" {
		q.Set("username", author)
	}
	if period := FilterOne(req, FacetPeriod); period != "" {
		q.Set("period", period)
	}
	for _, t := range Filter(req, FacetType) {
		q.Add("types", t)
	}
	for _, b := range Filter(req, FacetBaseModel) {
		q.Add("baseModels", b)
	}
	if tag := civFirstTag(req); tag != "" {
		q.Set("tag", tag)
	}
	var body struct {
		Items    []civModel `json:"items"`
		Metadata struct {
			NextCursor string `json:"nextCursor"`
			TotalItems uint64 `json:"totalItems"`
		} `json:"metadata"`
	}
	if _, err := c.JSON(ctx, c.URL("api", "v1", "models"), q, &body); err != nil {
		return nil, err
	}
	resp := &v1.SearchResponse{Hits: make([]*v1.SearchHit, 0, len(body.Items)), NextCursor: body.Metadata.NextCursor, Total: body.Metadata.TotalItems}
	for _, it := range body.Items {
		resp.Hits = append(resp.Hits, civHit(c, it))
	}
	return resp, nil
}

// Reads the nsfw switch from the request, hidden unless asked for
func civShowNSFW(req *v1.SearchRequest) bool {
	if v := FilterOne(req, FacetNSFW); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return false
}

// The API takes one tag, the facet wins over the request list
func civFirstTag(req *v1.SearchRequest) string {
	if t := FilterOne(req, FacetTag); t != "" {
		return t
	}
	for _, t := range req.GetTags() {
		if t = strings.TrimSpace(t); t != "" {
			return t
		}
	}
	return ""
}

func civHit(c *Client, it civModel) *v1.SearchHit {
	id := strconv.FormatInt(it.ID, 10)
	hit := &v1.SearchHit{
		Repo:        id,
		Name:        it.Name,
		Author:      it.Creator.Username,
		Downloads:   it.Stats.DownloadCount,
		Likes:       it.Stats.ThumbsUpCount,
		Tags:        it.Tags,
		Task:        it.Type,
		Description: Excerpt(StripTags(it.Description), 240),
		Url:         civPageURL(c, id),
		Extra:       map[string]string{},
	}
	if it.NSFW {
		hit.Extra["nsfw"] = "true"
	}
	if len(it.Versions) == 0 {
		return hit
	}
	v := it.Versions[0]
	if !v.PublishedAt.IsZero() {
		hit.UpdatedAt = timestamppb.New(v.PublishedAt)
	}
	if v.BaseModel != "" {
		hit.Extra["base_model"] = v.BaseModel
	}
	if v.Name != "" {
		hit.Extra["version"] = v.Name
	}
	for _, f := range v.Files {
		if f.Primary {
			hit.SizeBytes += civBytes(f)
		}
	}
	return hit
}

// Turns the API's fractional KB into exact bytes
func civBytes(f civFile) uint64 {
	return uint64(math.Round(f.SizeKB * 1024))
}

func civPageURL(c *Client, id string) string {
	return c.Base() + "/models/" + id
}

// Reads a model id and optional version id from a repo or a pasted page URL
func civParseRepo(repo string) (string, string, error) {
	repo = strings.TrimSpace(repo)
	if m := civRepoRe.FindStringSubmatch(repo); m != nil {
		return m[1], m[2], nil
	}
	if _, rest, ok := strings.Cut(repo, "/models/"); ok {
		rest, _, _ = strings.Cut(rest, "#")
		path, query, _ := strings.Cut(rest, "?")
		id, _, _ := strings.Cut(path, "/")
		if civDigits(id) {
			values, _ := url.ParseQuery(query)
			version := values.Get("modelVersionId")
			if !civDigits(version) {
				version = ""
			}
			return id, version, nil
		}
	}
	return "", "", fmt.Errorf("repo %q: want a model id such as 4201, 4201/501240, or a model page URL", repo)
}

func civDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func civGet(ctx context.Context, c *Client, id string) (*civModel, error) {
	var m civModel
	if _, err := c.JSON(ctx, c.URL("api", "v1", "models", id), nil, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Picks a version by id, then by name, else the first
func civPick(m *civModel, revision string) (*civVersion, error) {
	if len(m.Versions) == 0 {
		return nil, fmt.Errorf("model %d has no versions", m.ID)
	}
	if revision == "" {
		return &m.Versions[0], nil
	}
	for i := range m.Versions {
		if strconv.FormatInt(m.Versions[i].ID, 10) == revision {
			return &m.Versions[i], nil
		}
	}
	for i := range m.Versions {
		if m.Versions[i].Name == revision {
			return &m.Versions[i], nil
		}
	}
	for i := range m.Versions {
		if strings.EqualFold(m.Versions[i].Name, revision) {
			return &m.Versions[i], nil
		}
	}
	return nil, fmt.Errorf("model %d: version %q not found", m.ID, revision)
}

// Fetches the model named by repo and picks the version asked for
func civLookup(ctx context.Context, c *Client, repo, revision string) (string, *civVersion, error) {
	id, versionID, err := civParseRepo(repo)
	if err != nil {
		return "", nil, err
	}
	if revision = strings.TrimSpace(revision); revision == "" {
		revision = versionID
	}
	m, err := civGet(ctx, c, id)
	if err != nil {
		return "", nil, err
	}
	v, err := civPick(m, revision)
	if err != nil {
		return "", nil, err
	}
	return id, v, nil
}

func (civitaiAPI) Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error) {
	id, v, err := civLookup(ctx, c, repo, revision)
	if err != nil {
		return nil, err
	}
	vid := strconv.FormatInt(v.ID, 10)
	model := &v1.Model{Repo: id + "/" + vid, Revision: vid, Commit: "v" + vid}
	names := civArtifactNames(v.Files)
	for _, f := range v.Files {
		if f.Type == civTrainingData {
			continue
		}
		model.Artifacts = append(model.Artifacts, &v1.Artifact{Path: names[f.ID], SizeBytes: civBytes(f), Sha256: strings.ToLower(f.Hashes["SHA256"])})
	}
	return model, nil
}

func (civitaiAPI) Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error) {
	id, _, err := civParseRepo(repo)
	if err != nil {
		return nil, err
	}
	m, err := civGet(ctx, c, id)
	if err != nil {
		return nil, err
	}
	out := make([]*v1.Revision, 0, len(m.Versions))
	for i, v := range m.Versions {
		vid := strconv.FormatInt(v.ID, 10)
		r := &v1.Revision{Name: v.Name, Repo: id + "/" + vid, Commit: "v" + vid, Detail: v.BaseModel, Default: i == 0}
		if r.Name == "" {
			r.Name = vid
		}
		if !v.PublishedAt.IsZero() {
			r.UpdatedAt = timestamppb.New(v.PublishedAt)
		}
		for _, f := range v.Files {
			if f.Type != civTrainingData {
				r.SizeBytes += civBytes(f)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func (civitaiAPI) Card(ctx context.Context, c *Client, repo, revision string) (*v1.ModelCard, error) {
	id, _, err := civParseRepo(repo)
	if err != nil {
		return nil, err
	}
	m, err := civGet(ctx, c, id)
	if err != nil {
		return nil, err
	}
	return &v1.ModelCard{Html: m.Description, Url: civPageURL(c, id)}, nil
}

func (civitaiAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	_, v, err := civLookup(ctx, c, model.GetRepo(), model.GetRevision())
	if err != nil {
		return nil, err
	}
	names := civArtifactNames(v.Files)
	for _, f := range v.Files {
		if names[f.ID] != artifact.GetPath() {
			continue
		}
		rawURL := f.DownloadURL
		if rawURL == "" {
			rawURL = c.URL("api", "download", "models", strconv.FormatInt(v.ID, 10)) + "?fileId=" + strconv.FormatInt(f.ID, 10)
		}
		return c.Range(ctx, rawURL, artifact)
	}
	return nil, fmt.Errorf("%s: not in version %d", artifact.GetPath(), v.ID)
}
