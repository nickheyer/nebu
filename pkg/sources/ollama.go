package sources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The Ollama library, browsed on the site and pulled from its registry
var ollama = &Catalog{
	ID:   "ollama",
	Kind: v1.SourceKind_SOURCE_KIND_OLLAMA,
	Name: "Ollama",
	Transports: []Use{
		httpUse("https://ollama.com", "OLLAMA_API_KEY"),
		{Kind: TransportDistribution, Name: "registry", Fields: map[string]string{"endpoint": "https://registry.ollama.ai"}},
	},
	WebPath:       "/library",
	Description:   "Ollama library, pulled straight from its registry",
	RepoExample:   "llama3.2:3b",
	RepoPattern:   `^[\w.-]+(/[\w.-]+)?(:[\w.-]+)?$`,
	RevisionLabel: "tag",
	Sorts:         []string{SortDownloads, SortCreated},
	SortKeys:      map[string]string{SortDownloads: "popular", SortCreated: "newest"},
	Reversible:    []string{SortDownloads, SortCreated},
	Facets:        []*v1.Facet{olCapabilities()},
	MaxLimit:      200,
	HitFields:     []*v1.ConfigField{{Name: "sizes", Label: "Sizes", Description: "A size this model is published in, in parameters"}},
	API:           ollamaAPI{},
}

func init() { register(ollama) }

const (
	olNamespace     = "library"
	olPageMax       = 8 << 20
	olUpdatedLayout = "Jan 2, 2006 3:04 PM UTC"
	olLayerPrefix   = "application/vnd.ollama.image."
)

// Categories the library's c parameter accepts
func olCapabilities() *v1.Facet {
	facet := NewFacet(FacetCapability, "Capability", false)
	for _, c := range []string{"embedding", "vision", "tools", "thinking", "cloud"} {
		facet.Values = append(facet.Values, Value(c, ""))
	}
	return facet
}

// The Ollama library: a site scraped for listings, tags, cards, a registry for layers
type ollamaAPI struct{}

// Splits name[:tag] into the model name and its tag, an explicit revision wins
func olSplit(repo, revision string) (string, string) {
	return splitRef(repo, strings.TrimSpace(revision), latestTag)
}

// Returns the owner of a model, the library itself for bare names
func olAuthor(name string) string {
	if user, _, ok := strings.Cut(name, "/"); ok {
		return user
	}
	return olNamespace
}

var (
	olItemRe     = regexp.MustCompile(`(?s)<li\b[^>]*>(.*?)</li>`)
	olHrefRe     = regexp.MustCompile(`href="/library/([^"]+)"`)
	olDescRe     = regexp.MustCompile(`(?s)<p[^>]*max-w-lg[^>]*>(.*?)</p>`)
	olChipRe     = regexp.MustCompile(`(?s)<span[^>]*bg-indigo-50[^>]*>(.*?)</span>`)
	olSizeChipRe = regexp.MustCompile(`(?s)<span[^>]*bg-\[#ddf4ff\][^>]*>(.*?)</span>`)
	olPullsRe    = regexp.MustCompile(`(?s)<span[^>]*>\s*([^<]*?)\s*</span>\s*<span[^>]*>(?:\s|&nbsp;)*Pulls`)
	olTagsRe     = regexp.MustCompile(`(?s)<span[^>]*>\s*([^<]*?)\s*</span>\s*<span[^>]*>(?:\s|&nbsp;)*Tags`)
	olUpdatedRe  = regexp.MustCompile(`title="([^"]* UTC)"`)
)

func (ollamaAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	q := url.Values{"sort": {sort.Key}}
	query := strings.TrimSpace(req.GetQuery())
	if query != "" {
		q.Set("q", query)
	}
	if cap := FilterOne(req, FacetCapability); cap != "" {
		q.Set("c", cap)
	}
	page, err := c.Text(ctx, c.URL("library"), q, olPageMax)
	if err != nil {
		return nil, err
	}
	owner := Author(req)
	var hits []*v1.SearchHit
	for _, m := range olItemRe.FindAllStringSubmatch(page, -1) {
		hit := olHit(c, m[1])
		if hit == nil || !Matches(hit, query) || !olHasTags(hit, req.GetTags()) {
			continue
		}
		if owner != "" && !strings.EqualFold(hit.GetAuthor(), owner) {
			continue
		}
		hits = append(hits, hit)
	}
	// The site already orders the page, so ascending just flips it
	if sort.Ascending {
		slices.Reverse(hits)
	}
	return Page(hits, req, c.Limit(req)), nil
}

// Reads one library listing block into a hit
func olHit(c *Client, block string) *v1.SearchHit {
	m := olHrefRe.FindStringSubmatch(block)
	if m == nil {
		return nil
	}
	name := m[1]
	hit := newHit(name, name, olAuthor(name))
	hit.Url = c.URL(namespaced(name, olNamespace))
	if d := olDescRe.FindStringSubmatch(block); d != nil {
		hit.Description = Summary(d[1])
	}
	for _, chip := range olChipRe.FindAllStringSubmatch(block, -1) {
		if t := StripTags(chip[1]); t != "" {
			hit.Tags = append(hit.Tags, t)
		}
	}
	if len(hit.Tags) > 0 {
		hit.Task = hit.Tags[0]
	}
	var sizes []string
	for _, chip := range olSizeChipRe.FindAllStringSubmatch(block, -1) {
		if t := StripTags(chip[1]); t != "" {
			sizes = append(sizes, t)
		}
	}
	if len(sizes) > 0 {
		hit.Extra["sizes"] = strings.Join(sizes, ",")
	}
	if p := olPullsRe.FindStringSubmatch(block); p != nil {
		hit.Downloads = ParseCount(p[1])
	}
	if t := olTagsRe.FindStringSubmatch(block); t != nil {
		hit.Extra["tags"] = strings.TrimSpace(t[1])
	}
	if u := olUpdatedRe.FindStringSubmatch(block); u != nil {
		if ts, err := time.Parse(olUpdatedLayout, u[1]); err == nil {
			hit.UpdatedAt = timestamppb.New(ts)
		}
	}
	return hit
}

// Reports whether the hit carries every requested tag
func olHasTags(hit *v1.SearchHit, want []string) bool {
	for _, w := range want {
		w = strings.TrimSpace(w)
		if !slices.ContainsFunc(hit.GetTags(), func(t string) bool { return strings.EqualFold(t, w) }) {
			return false
		}
	}
	return true
}

var (
	olTagLinkRe = regexp.MustCompile(`<a[^>]*href="/([^"]+:[^"/]+)"`)
	olSizeRe    = regexp.MustCompile(`>\s*(\d+(?:\.\d+)?\s?[KMGT]?B)\s*<`)
	olContextRe = regexp.MustCompile(`>\s*(\d+(?:\.\d+)?[KM])\s*<`)
	olInputRe   = regexp.MustCompile(`(?s)<div[^>]*col-span-2[^>]*>\s*(.*?)\s*</div>`)
	olCommitRe  = regexp.MustCompile(`<span[^>]*font-mono[^>]*>\s*([0-9a-fA-F]{6,64})\s*</span>`)
)

func (ollamaAPI) Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error) {
	name, _ := olSplit(repo, "")
	page, err := c.Text(ctx, c.URL(namespaced(name, olNamespace), "tags"), nil, olPageMax)
	if err != nil {
		return nil, err
	}
	// Every tag is on the page twice, once per layout, so rows merge by tag
	prefix := namespaced(name, olNamespace) + ":"
	locs := olTagLinkRe.FindAllStringSubmatchIndex(page, -1)
	byTag := map[string]*v1.Revision{}
	var out []*v1.Revision
	for i, loc := range locs {
		full := page[loc[2]:loc[3]]
		if !strings.HasPrefix(full, prefix) {
			continue
		}
		tag := strings.TrimPrefix(full, prefix)
		end := len(page)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		rev := byTag[tag]
		if rev == nil {
			rev = &v1.Revision{Name: tag, Repo: name + ":" + tag, Default: tag == latestTag}
			byTag[tag] = rev
			out = append(out, rev)
		}
		olFill(rev, page[loc[1]:end])
	}
	return out, nil
}

// Reads size, context, input, and commit from one tag row, keeping what is already known
func olFill(rev *v1.Revision, chunk string) {
	if rev.SizeBytes == 0 {
		if m := olSizeRe.FindStringSubmatch(chunk); m != nil {
			rev.SizeBytes = ParseSize(m[1])
		}
	}
	if rev.Commit == "" {
		if m := olCommitRe.FindStringSubmatch(chunk); m != nil {
			rev.Commit = strings.ToLower(m[1])
		}
	}
	if rev.Detail == "" {
		var parts []string
		if m := olContextRe.FindStringSubmatch(chunk); m != nil {
			parts = append(parts, m[1]+" context")
		}
		if m := olInputRe.FindStringSubmatch(chunk); m != nil {
			if in := StripTags(m[1]); in != "" {
				parts = append(parts, in+" input")
			}
		}
		rev.Detail = strings.Join(parts, ", ")
	}
}

func (ollamaAPI) Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error) {
	name, tag := olSplit(repo, revision)
	if name == "" {
		return nil, fmt.Errorf("empty repo")
	}
	path := namespaced(name, olNamespace)
	m, err := c.Distribution().Manifest(ctx, path, tag)
	if err != nil {
		return nil, err
	}
	var cfg struct {
		FileType string `json:"file_type"`
	}
	if m.Config.Digest != "" {
		if err := c.Distribution().BlobJSON(ctx, path, m.Config.Digest, &cfg); err != nil {
			return nil, fmt.Errorf("%s:%s config: %w", name, tag, err)
		}
	}
	model := &v1.Model{Repo: name + ":" + tag, Revision: tag, Commit: m.Digest}
	if model.Commit == "" {
		model.Commit = olDigestOf(m.Layers)
	}
	base := strings.ReplaceAll(name, "/", "-") + "-" + tag
	names := namer{}
	for _, l := range m.Layers {
		model.Artifacts = append(model.Artifacts, &v1.Artifact{
			Path:      names.unique(olLayerName(base, cfg.FileType, l.MediaType)),
			SizeBytes: uint64(l.Size),
			Sha256:    Hex(l.Digest),
		})
	}
	return model, nil
}

// Hashes the layer digests so a manifest without one still has a commit
func olDigestOf(layers []Descriptor) string {
	h := sha256.New()
	for _, l := range layers {
		fmt.Fprintln(h, l.Digest)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Names a layer by its kind, weights carry the quant so GGUF rules group them
func olLayerName(base, fileType, mediaType string) string {
	kind := strings.TrimPrefix(mediaType, olLayerPrefix)
	if i := strings.LastIndex(kind, "."); i >= 0 {
		kind = kind[i+1:]
	}
	switch kind {
	case "model":
		if fileType != "" {
			return base + "-" + fileType + ".gguf"
		}
		return base + ".gguf"
	case "projector":
		return "mmproj-" + base + ".gguf"
	case "template":
		return "template.txt"
	case "params":
		return "params.json"
	case "system":
		return "system.txt"
	case "license":
		return "LICENSE"
	case "adapter":
		return "adapter.bin"
	case "":
		return "layer.bin"
	}
	return kind + ".bin"
}

var (
	olReadmeRe  = regexp.MustCompile(`(?s)<div[^>]*\bid="readme"[^>]*>(.*)`)
	olOgDescRe  = regexp.MustCompile(`<meta[^>]*property="og:description"[^>]*>`)
	olContentRe = regexp.MustCompile(`content="([^"]*)"`)
)

func (ollamaAPI) Card(ctx context.Context, c *Client, repo, revision string) (*v1.ModelCard, error) {
	name, _ := olSplit(repo, revision)
	page := c.URL(namespaced(name, olNamespace))
	text, err := c.Text(ctx, page, nil, cardMax)
	if err != nil {
		return cardOrEmpty(page, err)
	}
	card := &v1.ModelCard{Url: page}
	if m := olReadmeRe.FindStringSubmatch(text); m != nil {
		body := m[1]
		if i := strings.Index(body, "</main>"); i >= 0 {
			body = body[:i]
		}
		card.Html = strings.TrimSpace(body)
	}
	// The short description stands in when the page has no readme
	if card.Html == "" {
		card.Markdown = olDescription(text)
	}
	return card, nil
}

// Reads the og:description meta tag, whichever order its attributes come in
func olDescription(page string) string {
	tag := olOgDescRe.FindString(page)
	if tag == "" {
		return ""
	}
	m := olContentRe.FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(m[1]))
}

func (ollamaAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	if artifact.GetSizeBytes() == 0 {
		return nil, unknown(artifact.GetPath(), "size")
	}
	if artifact.GetSha256() == "" {
		return nil, unknown(artifact.GetPath(), "digest")
	}
	name, _ := olSplit(model.GetRepo(), model.GetRevision())
	return c.Distribution().Blob(namespaced(name, olNamespace), "sha256:"+artifact.GetSha256(), int64(artifact.GetSizeBytes()))
}
