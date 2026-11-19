package sources

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"path"
	"regexp"
	"slices"
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
	// Relevance sends no order, which ranks a text search by its match and lists by likes without
	// one. Highest Rated is left out since the API orders it exactly as Most Liked, and Recently
	// Added since it accepts that only inside a collection.
	Sorts: []string{SortDownloads, SortRelevance, SortTrending, SortLikes, civSortComments, civSortCollected, civSortImages, SortUpdated},
	SortKeys: map[string]string{
		SortDownloads:    "Most Downloaded",
		SortTrending:     "Hot",
		SortLikes:        "Most Liked",
		civSortComments:  "Most Discussed",
		civSortCollected: "Most Collected",
		// A text search ranks this one by its match
		civSortImages: "Most Images",
		// By the latest version's release, and by the model's creation in a text search
		SortUpdated: "Newest",
	},
	SortLabels: map[string]string{
		civSortComments:  "Most discussed",
		civSortCollected: "Most collected",
		civSortImages:    "Most images",
	},
	// Newest turns into Oldest, the API has no direction for the rest
	Reversible: []string{SortUpdated},
	Facets:     civFacets(),
	Columns: slices.Concat(
		Shown(Cols(ColSize, ColDownloads, ColLikes)...),
		Shown(ExtraCol("base_model", "Base models", AsList, "Base models the versions were made for")),
		[]*v1.HitColumn{
			Col(ColUpdated),
			Named(Col(ColTask), "Type"),
			Col(ColAuthor),
			ExtraCol(civSortComments, "Comments", AsCount, "Comments on the model"),
			ExtraCol("version", "Version", AsText, "The version listed first, which a download takes"),
			ExtraCol("commercial_use", "Commercial use", AsList, "Commercial uses the licence allows"),
			ExtraCol("no_credit", "No credit", AsFlag, "May be used without crediting the creator"),
			ExtraCol("merges", "Merges", AsFlag, "Merges of the model may be shared"),
			ExtraCol("generation", "Generates on Civitai", AsFlag, "The site's own generator runs it"),
			ExtraCol("paid", "Paid", AsFlag, "A version sits behind a live paywall or early access"),
			ExtraCol("nsfw", "NSFW", AsFlag, "Marked adult content by the source"),
		},
	),
	API: civitaiAPI{},
}

func init() { register(civitai) }

const civTrainingData = "Training Data"

// Sorts only Civitai offers
const (
	civSortComments  = "comments"
	civSortCollected = "collected"
	civSortImages    = "images"
)

// Facets only Civitai offers
const (
	civFacetCheckpoint  = "checkpoint"
	civFacetCommercial  = "commercial_use"
	civFacetFileFormat  = "file_format"
	civFacetAccess      = "access"
	civFacetTrained     = "trained"
	civFacetNewCreators = "new_creators"
)

// Model types the API listed on 2026-11-19, in its order, for narrowing types by kind
var civModelTypes = []string{"Checkpoint", "TextualInversion", "Hypernetwork", "AestheticGradient", "LORA", "LoCon", "DoRA", "Controlnet", "Upscaler", "MotionModule", "VAE", "TextEncoder", "UNet", "CLIPVision", "Poses", "Wildcards", "Workflows", "ComfyWorkflows", "Detection", "VisionLanguage", "CLIP", "LLM", "Other"}

// One value of a fixed facet: the API's name for it and the label shown
type civOption struct{ id, label string }

// Values the API's validation errors listed on 2026-11-19
var (
	// Periods since the latest version's release. All time is the same as none.
	civPeriods = []civOption{{"Day", "Last day"}, {"Week", "Last week"}, {"Month", "Last month"}, {"Year", "Last year"}}
	// Commercial uses in the model page's words. None is left out: models list it beside uses they
	// do allow, so it does not mean none.
	civCommercialUses = []civOption{{"Image", "Sell images"}, {"RentCivit", "Run on Civitai"}, {"Rent", "Run on paid services"}, {"Sell", "Sell the model"}, {"SellMerge", "Sell merges"}}
	civFileFormats    = []civOption{{"SafeTensor", "SafeTensors"}, {"PickleTensor", "Pickle"}, {"GGUF", ""}, {"Diffusers", ""}, {"Core ML", ""}, {"ONNX", ""}, {"Other", ""}}
)

// Paid access, by the switch the API turns on for each, each tried on 2026-11-19
var civAccess = []civOption{{"hidePaid", "Free"}, {"earlyAccess", "Early access"}, {"paidAccess", "Paid"}, {"onSale", "On sale"}}

// Filters the API turns on with true, by facet. Its supportsGeneration is left out: it also cuts
// each model's versions down to the ones the site's generator covers, often none, leaving hits
// without sizes, formats, or files.
var civSwitches = []struct{ facet, param, label, hint string }{
	{civFacetTrained, "fromPlatform", "Trained on Civitai", "Models with a version made by the site's trainer"},
	{civFacetNewCreators, "newCreators", "New creators", "Models by creators on the site's new and upcoming board"},
}

var civRepoRe = regexp.MustCompile(`^(\d+)(?:/(\d+))?$`)

// The Civitai API: models with versions as variants, cursor paging, and signed downloads
type civitaiAPI struct{}

// Builds a facet over fixed values
func civChoice(id, label string, multi bool, options []civOption) *v1.Facet {
	f := NewFacet(id, label, multi)
	for _, o := range options {
		f.Values = append(f.Values, Value(o.id, o.label))
	}
	return f
}

// Fixed facets, after the types and base models the API lists
func civFacets() []*v1.Facet {
	out := []*v1.Facet{
		Hint(civChoice(FacetPeriod, "Updated within", false, civPeriods), "Models with a version released in the period"),
		Hint(NewFacet(civFacetCheckpoint, "Checkpoint", false, Value("Trained", ""), Value("Merge", "Merged")), "Checkpoints trained or merged, other types pass through"),
		Hint(civChoice(civFacetFileFormat, "File format", true, civFileFormats), "Models with a file in any of these formats"),
		Hint(civChoice(civFacetCommercial, "Commercial use", true, civCommercialUses), "Models whose licence allows any of these"),
		Hint(civChoice(civFacetAccess, "Access", false, civAccess), "Free leaves out models behind a live paywall, the others list only models in early access, priced, or on sale"),
	}
	for _, s := range civSwitches {
		out = append(out, Hint(Toggle(s.facet, s.label), s.hint))
	}
	return append(out,
		Hint(Toggle(FacetNSFW, "NSFW"), "Lists models rated for adults too"),
		Hint(Freeform(FacetTag, "Tag"), "Models carrying this tag"),
		Hint(Freeform(FacetAuthor, "Author"), "A creator's username"),
	)
}

// Reads the model types and base models the API publishes
func (civitaiAPI) Facets(ctx context.Context, c *Client) ([]*v1.Facet, error) {
	var enums struct {
		ModelType []string `json:"ModelType"`
		BaseModel []string `json:"BaseModel"`
	}
	if _, err := c.JSON(ctx, c.URL("api", "v1", "enums"), nil, &enums); err != nil {
		return nil, err
	}
	types := NewFacet(FacetType, "Type", true)
	for _, t := range enums.ModelType {
		types.Values = append(types.Values, Value(t, t))
	}
	bases := NewFacet(FacetBaseModel, "Base model", true)
	for _, b := range enums.BaseModel {
		bases.Values = append(bases.Values, Value(b, b))
	}
	return []*v1.Facet{types, Hint(bases, "Models with a version made for any of these")}, nil
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
		Size string `json:"size"`
		FP   string `json:"fp"`
	} `json:"metadata"`
}

// Names a version's files, splicing precision, size, then the id into names that collide
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
			if len(parts) == 0 || seen[withSuffix(f.Name, "."+strings.Join(parts, "-"))] {
				parts = append(parts, strconv.FormatInt(f.ID, 10))
			}
			name = withSuffix(f.Name, "."+strings.Join(parts, "-"))
		}
		seen[name] = true
		out[f.ID] = name
	}
	return out
}

type civVersion struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	BaseModel   string    `json:"baseModel"`
	PublishedAt time.Time `json:"publishedAt"`
	Files       []civFile `json:"files"`
}

type civModel struct {
	ID                 int64    `json:"id"`
	Name               string   `json:"name"`
	Type               string   `json:"type"`
	NSFW               bool     `json:"nsfw"`
	Tags               []string `json:"tags"`
	Description        string   `json:"description"`
	BaseModels         []string `json:"baseModels"`
	AllowNoCredit      bool     `json:"allowNoCredit"`
	AllowDerivatives   bool     `json:"allowDerivatives"`
	AllowCommercialUse []string `json:"allowCommercialUse"`
	SupportsGeneration bool     `json:"supportsGeneration"`
	Paid               bool     `json:"hasActivePaidAccess"`
	Stats              struct {
		DownloadCount uint64 `json:"downloadCount"`
		ThumbsUpCount uint64 `json:"thumbsUpCount"`
		CommentCount  uint64 `json:"commentCount"`
	} `json:"stats"`
	Creator struct {
		Username string `json:"username"`
	} `json:"creator"`
	Versions []civVersion `json:"modelVersions"`
}

func (civitaiAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	q := url.Values{
		"limit": {strconv.Itoa(c.Limit(req))},
		"nsfw":  {strconv.FormatBool(On(req, FacetNSFW))},
	}
	if key := sort.Key; key != "" {
		// Only the newest order can be flipped, the API has no direction switch
		if sort.Ascending {
			key = "Oldest"
		}
		q.Set("sort", key)
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
	types, any := civTypesFor(req)
	if !any {
		return &v1.SearchResponse{}, nil
	}
	for _, t := range types {
		q.Add("types", t)
	}
	for _, b := range Filter(req, FacetBaseModel) {
		q.Add("baseModels", b)
	}
	tag := civFirstTag(req)
	if tag != "" {
		q.Set("tag", tag)
	}
	if err := civFilters(req, q); err != nil {
		return nil, err
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
	// The API drops a tag it does not know and lists every model, so a hit without it means it was dropped
	if tag != "" && slices.ContainsFunc(body.Items, func(m civModel) bool {
		return !slices.ContainsFunc(m.Tags, func(t string) bool { return strings.EqualFold(t, tag) })
	}) {
		return nil, fmt.Errorf("%w: Civitai has no tag %q", ErrSource, tag)
	}
	resp := &v1.SearchResponse{Hits: make([]*v1.SearchHit, 0, len(body.Items)), NextCursor: body.Metadata.NextCursor, Total: body.Metadata.TotalItems}
	for _, it := range body.Items {
		resp.Hits = append(resp.Hits, civHit(c, it))
	}
	return resp, nil
}

// The model types a request admits: the type facet narrowed to the kinds asked for, every type
// of those kinds when no type is named. Reports false when kinds and types share nothing, since
// asking the API for no type lists every type.
func civTypesFor(req *v1.SearchRequest) ([]string, bool) {
	types := Filter(req, FacetType)
	kinds, err := Kinds(req)
	if err != nil || len(kinds) == 0 {
		return types, true
	}
	var out []string
	for _, t := range civModelTypes {
		k, ok := civitaiKinds[t]
		if ok && slices.Contains(kinds, k) && (len(types) == 0 || slices.Contains(types, t)) {
			out = append(out, t)
		}
	}
	return out, len(out) > 0
}

// Adds the checkpoint, file, licence, access, and switch filters
func civFilters(req *v1.SearchRequest, q url.Values) error {
	if t := FilterOne(req, civFacetCheckpoint); t != "" {
		q.Set("checkpointType", t)
	}
	for _, u := range Filter(req, civFacetCommercial) {
		q.Add("allowCommercialUse", u)
	}
	// The API reads file formats only as a list, so a lone one goes twice
	formats := Filter(req, civFacetFileFormat)
	if len(formats) == 1 {
		formats = append(formats, formats[0])
	}
	for _, f := range formats {
		q.Add("fileFormats", f)
	}
	// Each access value is a switch of its own, so one the API does not name would go unread
	if a := FilterOne(req, civFacetAccess); a != "" {
		if !slices.ContainsFunc(civAccess, func(o civOption) bool { return o.id == a }) {
			return fmt.Errorf("%w: %s %q is none of hidePaid, earlyAccess, paidAccess, or onSale", ErrSource, civFacetAccess, a)
		}
		q.Set(a, "true")
	}
	for _, s := range civSwitches {
		if On(req, s.facet) {
			q.Set(s.param, "true")
		}
	}
	return nil
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
	hit := newHit(id, it.Name, it.Creator.Username)
	hit.Downloads, hit.Likes, hit.Tags, hit.Task = it.Stats.DownloadCount, it.Stats.ThumbsUpCount, it.Tags, it.Type
	hit.Description, hit.Url = Summary(it.Description), civPageURL(c, id)
	for key, on := range map[string]bool{"nsfw": it.NSFW, "no_credit": it.AllowNoCredit, "merges": it.AllowDerivatives, "generation": it.SupportsGeneration, "paid": it.Paid} {
		if on {
			hit.Extra[key] = "true"
		}
	}
	if it.Stats.CommentCount > 0 {
		hit.Extra[civSortComments] = strconv.FormatUint(it.Stats.CommentCount, 10)
	}
	if len(it.BaseModels) > 0 {
		hit.Extra["base_model"] = strings.Join(it.BaseModels, ",")
	}
	var uses []string
	for _, u := range civCommercialUses {
		if slices.Contains(it.AllowCommercialUse, u.id) {
			uses = append(uses, u.label)
		}
	}
	if len(uses) > 0 {
		hit.Extra["commercial_use"] = strings.Join(uses, ",")
	}
	if len(it.Versions) == 0 {
		return hit
	}
	// Updated when the newest version came out, which need not be the one listed first
	var latest time.Time
	for _, v := range it.Versions {
		if v.PublishedAt.After(latest) {
			latest = v.PublishedAt
		}
	}
	if !latest.IsZero() {
		hit.UpdatedAt = timestamppb.New(latest)
	}
	v := it.Versions[0]
	if v.Name != "" {
		hit.Extra["version"] = v.Name
	}
	for _, f := range v.Files {
		if f.Primary {
			hit.SizeBytes += civBytes(f)
		}
		// Civitai publishes single file checkpoints, safetensors and pickles, and the odd GGUF
		if format := civFormat(f.Name, it.Type); format != "" && !slices.Contains(hit.Formats, format) {
			hit.Formats = append(hit.Formats, format)
		}
	}
	return hit
}

// Names the format a file holds. Language models keep their safetensors as the transformers
// layout, every other safetensors or pickle is a single file diffusion checkpoint.
func civFormat(name, modelType string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".gguf":
		return "gguf"
	case ".safetensors", ".sft":
		if civitaiKinds[modelType] == v1.ModelKind_MODEL_KIND_LANGUAGE {
			return "safetensors"
		}
		return "diffusion"
	case ".ckpt", ".pt", ".pth":
		return "diffusion"
	}
	return ""
}

// Turns the API's fractional KB into exact bytes
func civBytes(f civFile) uint64 {
	return uint64(math.Round(f.SizeKB * 1024))
}

// The model page on the site
func civPageURL(c *Client, id string) string { return c.Page("models", id) }

// Reads a model id and optional version id from a repo or a page URL
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

// How long a model's detail is kept, so the calls one page makes share it
const civDetailTTL = 10 * time.Minute

func civGet(ctx context.Context, c *Client, id string) (*civModel, error) {
	memo := Cached(c, "civitai-model:"+id, func() *Memo[*civModel] { return &Memo[*civModel]{TTL: civDetailTTL} })
	return memo.Get(ctx, func(ctx context.Context) (*civModel, error) {
		var m civModel
		if _, err := c.JSON(ctx, c.URL("api", "v1", "models", id), nil, &m); err != nil {
			return nil, err
		}
		return &m, nil
	})
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
		model.Artifacts = append(model.Artifacts, &v1.Artifact{Path: names[f.ID], SizeBytes: civBytes(f), Sha256: Hex(f.Hashes["SHA256"]), Url: civFileURL(c, v.ID, f)})
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
	return &v1.ModelCard{Html: m.Description, Url: civPageURL(c, id), Hit: civHit(c, *m)}, nil
}

// Where a file of a version downloads from, the address the API names or the download endpoint by id
func civFileURL(c *Client, versionID int64, f civFile) string {
	if f.DownloadURL != "" {
		return f.DownloadURL
	}
	return c.URL("api", "download", "models", strconv.FormatInt(versionID, 10)) + "?fileId=" + strconv.FormatInt(f.ID, 10)
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
		rawURL := civFileURL(c, v.ID, f)
		return c.Range(ctx, rawURL, artifact)
	}
	return nil, fmt.Errorf("%s: not in version %d", artifact.GetPath(), v.ID)
}
