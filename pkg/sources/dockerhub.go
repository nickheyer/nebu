package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Docker Hub, model artifacts in its ai/ namespace
var dockerhub = &Catalog{
	ID:   "dockerhub",
	Kind: v1.SourceKind_SOURCE_KIND_OCI,
	Name: "OCI registry",
	Seed: "Docker Hub",
	// The hub API browses and searches, a bare registry resolves and pulls by name
	Transports: []Use{
		{Kind: TransportHTTP, Fields: map[string]string{"endpoint": ""}},
		{Kind: TransportDistribution, Name: "registry", Fields: map[string]string{"endpoint": "https://registry-1.docker.io", "token_env": "DOCKER_TOKEN"}},
	},
	SeedConfig:    map[string]string{"endpoint": "https://hub.docker.com"},
	Description:   "Docker Hub, model artifacts under its ai/ namespace, or any registry speaking the OCI distribution API",
	RepoExample:   "ai/gemma3:4b-q4_K_M",
	RepoPattern:   `^[\w.-]+(/[\w.-]+)+(:[\w.-]+)?$`,
	RevisionLabel: "tag",
	Sorts:         []string{SortDownloads, SortUpdated},
	SortKeys:      map[string]string{SortDownloads: "pull_count", SortUpdated: "updated_at"},
	Reversible:    []string{SortDownloads, SortUpdated},
	Facets:        []*v1.Facet{Freeform(FacetAuthor, "Author")},
	API:           dockerhubAPI{},
}

func init() { register(dockerhub) }

const (
	dhNamespace   = "ai"
	dhMaxPages    = 10
	dhFilepathKey = "org.cncf.model.filepath"
)

// Layer kinds by media type, used to name layers that carry no file path
const (
	dhKindOther = iota
	dhKindGGUF
	dhKindProjector
	dhKindLicense
	dhKindDoc
	dhKindWeight
)

// Docker Hub: the hub API for search, tags, and cards, the registry for layers
type dockerhubAPI struct{}

type dhItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Publisher struct {
		Name string `json:"name"`
	} `json:"publisher"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
	ShortDescription string          `json:"short_description"`
	StarCount        uint64          `json:"star_count"`
	PullCount        json.RawMessage `json:"pull_count"`
	RawPullCount     uint64          `json:"raw_pull_count"`
	ContentTypes     []string        `json:"content_types"`
	MediaTypes       []string        `json:"media_types"`
}

// Only a source naming a hub API lists, searches, or reads cards
func (dockerhubAPI) Browses(c *Client) bool { return c.HTTP() != nil }

// Searches the hub, scoped to the author or the namespace unless the query names one
func (dockerhubAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	if c.HTTP() == nil {
		return nil, fmt.Errorf("%w: %s names no hub endpoint to search", ErrUnsupported, c.ID())
	}
	from := Offset(req.GetCursor())
	query := strings.TrimSpace(req.GetQuery())
	if !strings.Contains(query, "/") {
		prefix := Author(req)
		if prefix == "" {
			prefix = dhNamespace
		}
		query = prefix + "/" + query
	}
	q := url.Values{
		"query": {query},
		"type":  {"model"},
		"size":  {strconv.Itoa(c.Limit(req))},
		"from":  {strconv.Itoa(from)},
		"sort":  {sort.Key},
		"order": {direction(sort.Ascending, "asc", "desc")},
	}
	var body struct {
		Total   uint64   `json:"total"`
		Results []dhItem `json:"results"`
	}
	if _, err := c.JSON(ctx, c.URL("api", "search", "v4"), q, &body); err != nil {
		return nil, err
	}
	resp := &v1.SearchResponse{Total: body.Total}
	for _, it := range body.Results {
		if !dhIsModel(it) {
			continue
		}
		hit := dhHit(c, it)
		if AdmitsFormats(req, hit.GetFormats()) {
			resp.Hits = append(resp.Hits, hit)
		}
	}
	if next := from + len(body.Results); len(body.Results) > 0 && uint64(next) < body.Total {
		resp.NextCursor = strconv.Itoa(next)
	}
	return resp, nil
}

// Keeps hub results that hold a model rather than an image
func dhIsModel(it dhItem) bool {
	for _, t := range it.ContentTypes {
		if strings.EqualFold(t, "model") {
			return true
		}
	}
	for _, m := range it.MediaTypes {
		if strings.Contains(strings.ToLower(m), "model") {
			return true
		}
	}
	return false
}

func dhHit(c *Client, it dhItem) *v1.SearchHit {
	repo := it.ID
	if repo == "" {
		repo = it.Slug
	}
	if repo == "" {
		repo = it.Name
	}
	author, name := splitRepo(repo)
	if it.Publisher.Name != "" {
		author = it.Publisher.Name
	}
	hit := &v1.SearchHit{
		Repo:        repo,
		Name:        name,
		Author:      author,
		Downloads:   it.RawPullCount,
		Likes:       it.StarCount,
		Description: Summary(it.ShortDescription),
		Url:         dhPage(c, repo),
		UpdatedAt:   Stamp(it.UpdatedAt),
		CreatedAt:   Stamp(it.CreatedAt),
	}
	if hit.Downloads == 0 {
		hit.Downloads = ParseCount(strings.TrimSuffix(dhRawText(it.PullCount), "+"))
	}
	// The listing names layer media types, which say GGUF before the manifest is read.
	for _, mt := range it.MediaTypes {
		if dhMediaKind(mt) == dhKindGGUF {
			hit.Formats = []string{"gguf"}
			break
		}
	}
	return hit
}

// The repository page on the hub
func dhPage(c *Client, name string) string { return c.Page("r", name) }

// Reads a JSON value that may be a string or a number as text
func dhRawText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

// Splits name:tag, an explicit revision wins, latest and the ai namespace fill in
func dhSplit(repo, revision string) (string, string) {
	name, tag := splitRef(repo, revision, latestTag)
	return namespaced(name, dhNamespace), tag
}

func (dockerhubAPI) Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error) {
	name, tag := dhSplit(repo, revision)
	m, err := c.Distribution().Manifest(ctx, name, tag)
	if err != nil {
		return nil, err
	}
	return &v1.Model{Repo: name + ":" + tag, Revision: tag, Commit: m.Digest, Artifacts: dhArtifacts(ctx, c, name, tag, m)}, nil
}

// Turns manifest layers into artifacts, naming the ones that carry no file path
func dhArtifacts(ctx context.Context, c *Client, name, tag string, m *Manifest) []*v1.Artifact {
	stem := name[strings.LastIndex(name, "/")+1:] + "-" + tag
	format, loaded := "", false
	names := namer{}
	var out []*v1.Artifact
	for i, l := range m.Layers {
		if l.Size <= 0 {
			continue
		}
		p := dhAnnotated(l.Annotations)
		if p == "" {
			kind := dhMediaKind(l.MediaType)
			if kind == dhKindWeight && !loaded {
				format, loaded = dhFormat(ctx, c, name, m.Config), true
			}
			p = dhGenerated(kind, stem, format, i+1)
		}
		a := &v1.Artifact{Path: names.unique(p), SizeBytes: uint64(l.Size)}
		if strings.HasPrefix(strings.ToLower(l.Digest), "sha256:") {
			a.Sha256 = Hex(l.Digest)
		}
		out = append(out, a)
	}
	return out
}

// Reads the file path a layer is annotated with, cleaned of leading slashes and dots
func dhAnnotated(a map[string]string) string {
	for _, key := range []string{dhFilepathKey, titleKey} {
		if v := strings.TrimSpace(a[key]); v != "" {
			if p := strings.TrimPrefix(path.Clean("/"+v), "/"); p != "" {
				return p
			}
		}
	}
	return ""
}

// Classifies a layer by media type, weight layers need the config to name their format
func dhMediaKind(mediaType string) int {
	mt := strings.ReplaceAll(strings.ToLower(mediaType), "docker", "")
	switch {
	case strings.Contains(mt, "mmproj"), strings.Contains(mt, "projector"):
		return dhKindProjector
	case strings.Contains(mt, "gguf"):
		return dhKindGGUF
	case strings.Contains(mt, "license"):
		return dhKindLicense
	case strings.Contains(mt, "doc"), strings.Contains(mt, "readme"):
		return dhKindDoc
	case strings.Contains(mt, "weight"), strings.Contains(mt, "model"):
		return dhKindWeight
	}
	return dhKindOther
}

// Names a layer from its kind, n is its position when nothing describes it
func dhGenerated(kind int, stem, format string, n int) string {
	switch kind {
	case dhKindGGUF:
		return stem + ".gguf"
	case dhKindProjector:
		return "mmproj-" + stem + ".gguf"
	case dhKindLicense:
		return "LICENSE"
	case dhKindDoc:
		return "README.md"
	case dhKindWeight:
		if format == "gguf" {
			return stem + ".gguf"
		}
	}
	return fmt.Sprintf("%s-%d.bin", stem, n)
}

// Reads the weight format the config declares, empty when it cannot be read
func dhFormat(ctx context.Context, c *Client, name string, cfg Descriptor) string {
	if cfg.Digest == "" {
		return ""
	}
	var body struct {
		Format string `json:"format"`
		Config struct {
			Format string `json:"format"`
		} `json:"config"`
	}
	if err := c.Distribution().BlobJSON(ctx, name, cfg.Digest, &body); err != nil {
		return ""
	}
	if body.Config.Format != "" {
		return strings.ToLower(body.Config.Format)
	}
	return strings.ToLower(body.Format)
}

func (dockerhubAPI) Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error) {
	name, _ := dhSplit(repo, "")
	tags, err := c.Distribution().Tags(ctx, name)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(tags, func(i, j int) bool {
		if tags[i] == latestTag || tags[j] == latestTag {
			return tags[i] == latestTag && tags[j] != latestTag
		}
		return dhNaturalLess(tags[i], tags[j])
	})
	out := make([]*v1.Revision, 0, len(tags))
	byName := map[string]*v1.Revision{}
	for _, t := range tags {
		r := &v1.Revision{Name: t, Repo: name + ":" + t, Default: t == latestTag}
		out = append(out, r)
		byName[t] = r
	}
	if err := dhDecorate(ctx, c, name, byName); err != nil {
		return nil, err
	}
	return out, nil
}

// Adds tag sizes, dates, and digests from Docker Hub when a hub endpoint is configured.
func dhDecorate(ctx context.Context, c *Client, name string, byName map[string]*v1.Revision) error {
	if c.HTTP() == nil {
		return nil
	}
	next := c.URL("v2", "repositories", name, "tags") + "?page_size=100"
	for page := 0; next != "" && page < dhMaxPages; page++ {
		var body struct {
			Next    string `json:"next"`
			Results []struct {
				Name        string `json:"name"`
				FullSize    uint64 `json:"full_size"`
				LastUpdated string `json:"last_updated"`
				Digest      string `json:"digest"`
			} `json:"results"`
		}
		if _, err := c.JSON(ctx, next, nil, &body); err != nil {
			return fmt.Errorf("%s tags: %w", name, err)
		}
		for _, t := range body.Results {
			r, ok := byName[t.Name]
			if !ok {
				continue
			}
			r.SizeBytes = t.FullSize
			r.UpdatedAt = Stamp(t.LastUpdated)
			r.Commit = t.Digest
		}
		next = body.Next
	}
	return nil
}

// Orders tags so 7b comes before 12b, digit runs compare by value
func dhNaturalLess(a, b string) bool {
	for a != "" && b != "" {
		ah, at := dhChunk(a)
		bh, bt := dhChunk(b)
		if ah != bh {
			if dhDigits(ah) && dhDigits(bh) {
				if c := dhNumCompare(ah, bh); c != 0 {
					return c < 0
				}
			} else if al, bl := strings.ToLower(ah), strings.ToLower(bh); al != bl {
				return al < bl
			} else {
				return ah < bh
			}
		}
		a, b = at, bt
	}
	return len(a) < len(b)
}

// Splits off the leading run of digits or non digits
func dhChunk(s string) (string, string) {
	digit := s[0] >= '0' && s[0] <= '9'
	i := 1
	for i < len(s) && (s[i] >= '0' && s[i] <= '9') == digit {
		i++
	}
	return s[:i], s[i:]
}

func dhDigits(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}

func dhNumCompare(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

func (dockerhubAPI) Card(ctx context.Context, c *Client, repo, revision string) (*v1.ModelCard, error) {
	if c.HTTP() == nil {
		return nil, fmt.Errorf("%w: %s names no hub endpoint to read cards from", ErrUnsupported, c.ID())
	}
	name, _ := dhSplit(repo, revision)
	ns, short, _ := strings.Cut(name, "/")
	page := dhPage(c, name)
	var body struct {
		Description     string `json:"description"`
		FullDescription string `json:"full_description"`
	}
	if _, err := c.JSON(ctx, c.URL("v2", "namespaces", ns, "repositories", short), nil, &body); err != nil {
		return cardOrEmpty(page, err)
	}
	card := &v1.ModelCard{Url: page}
	text := strings.TrimSpace(body.FullDescription)
	if text == "" {
		text = strings.TrimSpace(body.Description)
	}
	if strings.HasPrefix(text, "<") {
		card.Html = text
	} else {
		card.Markdown = text
	}
	return card, nil
}

func (d dockerhubAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	if artifact.GetSizeBytes() == 0 {
		return nil, unknown(artifact.GetPath(), "size")
	}
	name, _ := dhSplit(model.GetRepo(), model.GetRevision())
	digest := artifact.GetSha256()
	if digest == "" {
		// An artifact without its digest is looked up again by path
		resolved, err := d.Resolve(ctx, c, model.GetRepo(), model.GetRevision())
		if err != nil {
			return nil, err
		}
		for _, a := range resolved.GetArtifacts() {
			if a.GetPath() == artifact.GetPath() {
				digest = a.GetSha256()
			}
		}
		if digest == "" {
			return nil, unknown(artifact.GetPath(), "digest")
		}
	}
	return c.Distribution().Blob(name, "sha256:"+digest, int64(artifact.GetSizeBytes()))
}
