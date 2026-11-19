package sources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/sync/errgroup"
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
	// The library page orders by pulls and by age. The search page orders by a popularity of its own
	// that is not by pulls, by age, by name, and by context or price, which rank cloud models and
	// leave the rest after them. Both pages call their popular order popular.
	Sorts: []string{SortDownloads, SortTrending, SortCreated, SortName, olSortContext, olSortPrice},
	SortKeys: map[string]string{
		SortDownloads: "popular",
		SortCreated:   "newest",
		SortTrending:  "popular",
		SortName:      "name",
		olSortContext: "context",
		olSortPrice:   "price",
	},
	SortLabels: map[string]string{SortTrending: "Popular", olSortContext: "Largest context", olSortPrice: "Lowest price"},
	// The library page lists the whole library at once, so flipping it is the exact ascending order.
	// The search page pages its results and has no direction.
	Reversible: olLibrarySorts,
	Facets:     olFacets(),
	MaxLimit:   200,
	Columns: slices.Concat(
		Shown(Cols(ColDownloads, ColUpdated)...),
		Shown(
			ExtraCol("capabilities", "Capabilities", AsList, "What the site says the model can do, such as tools, vision, or thinking"),
			ExtraCol("sizes", "Sizes", AsList, "Sizes the model is published in, in parameters"),
		),
		[]*v1.HitColumn{
			Col(ColAuthor),
			ExtraCol("tags", "Tags", AsCount, "Tags published, one per size and quantization"),
			ExtraCol("cloud", "Cloud", AsFlag, "Ollama's cloud runs it"),
			ExtraCol(olSortContext, "Context", AsCount, "Context window in tokens, which the search page shows for cloud models"),
			ExtraCol(olSortPrice, "Price", AsText, "Usage cost on Ollama's cloud, from Low to Extra High"),
		},
	),
	API: ollamaAPI{},
}

func init() { register(ollama) }

const (
	olNamespace     = "library"
	olPageMax       = 8 << 20
	olUpdatedLayout = "Jan 2, 2006 3:04 PM UTC"
	olUpdatedDay    = "January 2, 2006"
	olLayerPrefix   = "application/vnd.ollama.image."
)

// Sorts only Ollama offers
const (
	olSortContext = "context"
	olSortPrice   = "price"
)

// Facets only Ollama offers
const (
	olFacetWhere = "where"
	olFacetSize  = "size"
)

// Sorts the library page serves, the rest are the search page's
var olLibrarySorts = []string{SortDownloads, SortCreated}

// One download size the search page filters by: its value and the label the page shows
type olSize struct{ id, label string }

// The choices of the search page's form, read from it on 2026-11-19
var (
	olCapabilities = []string{"tools", "thinking", "vision", "embedding", "decision"}
	olWhere        = []string{"local", "cloud"}
	olSizes        = []olSize{{"8", "Up to 8 GB"}, {"16", "Up to 16 GB"}, {"32", "Up to 32 GB"}, {"64", "Up to 64 GB"}, {"over64", "Over 64 GB"}}
)

// The search page's filters
func olFacets() []*v1.Facet {
	caps := NewFacet(FacetCapability, "Capabilities", true)
	for _, c := range olCapabilities {
		caps.Values = append(caps.Values, Value(c, ""))
	}
	where := NewFacet(olFacetWhere, "Where", false)
	for _, w := range olWhere {
		where.Values = append(where.Values, Value(w, ""))
	}
	sizes := NewFacet(olFacetSize, "Download size", false)
	for _, s := range olSizes {
		sizes.Values = append(sizes.Values, Value(s.id, s.label))
	}
	return []*v1.Facet{
		Hint(caps, "Models with every capability picked"),
		Hint(where, "Models that download to run here, or that Ollama's cloud runs. Some do both."),
		Hint(sizes, "Models with a tag whose download fits, counting local tags only"),
	}
}

// The search page's parameters for the request's filters. The page ignores a value it does not know,
// and a size beside cloud, so those are refused here.
func olFilters(req *v1.SearchRequest) (url.Values, error) {
	q := url.Values{}
	for _, cap := range Filter(req, FacetCapability) {
		if !slices.Contains(olCapabilities, cap) {
			return nil, fmt.Errorf("%w: %s %q is none of %s", ErrSource, FacetCapability, cap, strings.Join(olCapabilities, ", "))
		}
		q.Add("c", cap)
	}
	where := FilterOne(req, olFacetWhere)
	if where != "" {
		if !slices.Contains(olWhere, where) {
			return nil, fmt.Errorf("%w: %s %q is none of %s", ErrSource, olFacetWhere, where, strings.Join(olWhere, ", "))
		}
		q.Add("c", where)
	}
	if size := FilterOne(req, olFacetSize); size != "" {
		if !slices.ContainsFunc(olSizes, func(s olSize) bool { return s.id == size }) {
			return nil, fmt.Errorf("%w: %s %q is none of 8, 16, 32, 64, or over64", ErrSource, olFacetSize, size)
		}
		if where == "cloud" {
			return nil, fmt.Errorf("%w: Ollama sizes local downloads only, not cloud models", ErrSource)
		}
		q.Set("s", size)
	}
	return q, nil
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
	olCloudRe    = regexp.MustCompile(`(?s)<span[^>]*bg-cyan-50[^>]*>\s*cloud\s*</span>`)
	// The model page counts downloads where the listing counts pulls
	olDownloadsRe = regexp.MustCompile(`(?s)<span[^>]*>\s*([^<]*?)\s*</span>\s*<span[^>]*>(?:\s|&nbsp;)*Downloads`)
)

// What a search page result shows, which a library listing words its own way
var (
	// The model a result links to, a library model or a community one under its owner
	olResultRe     = regexp.MustCompile(`^\s*<a href="/([\w.-]+(?:/[\w.-]+)?)" class="group`)
	olSummaryRe    = regexp.MustCompile(`(?s)<p[^>]*line-clamp-2[^>]*>(.*?)</p>`)
	olCountRe      = regexp.MustCompile(`title="([\d,]+) downloads"`)
	olTagCountRe   = regexp.MustCompile(`<span[^>]*>\s*([\d,]+) tags?\s*</span>`)
	olUpdatedDayRe = regexp.MustCompile(`title="Updated ([A-Z][a-z]+ \d{1,2}, \d{4})"`)
	olContextRe    = regexp.MustCompile(`(?s)title="Context window">.*?<span[^>]*>\s*([^<]*?)\s*</span>`)
	olPriceRe      = regexp.MustCompile(`title="([^"]+) usage"`)
	// The link the page's script follows for the next page, there while more remain
	olMoreRe = regexp.MustCompile(`hx-get="/search\?page=\d+`)
)

// Asks for the search page as its own script does, which is answered with the results alone and
// is the only way past the first page
var olScript = http.Header{"HX-Request": {"true"}}

const (
	// How long the library models a filter admits are kept
	olMembersTTL = 10 * time.Minute
	// Search pages read for a filter's members before giving up, the whole library filling fourteen
	olMembersPages = 50
)

// Searches the library page for its two orders and the search page for the rest. Every entry is a
// GGUF language model, so a request for another format or kind has nothing to fetch.
func (ollamaAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	if !AdmitsFormats(req, []string{"gguf"}) || !AdmitsKind(req, v1.ModelKind_MODEL_KIND_LANGUAGE) {
		return &v1.SearchResponse{}, nil
	}
	filters, err := olFilters(req)
	if err != nil {
		return nil, err
	}
	if slices.Contains(olLibrarySorts, sort.ID) {
		return olLibrary(ctx, c, req, sort, filters)
	}
	return olSearch(ctx, c, req, sort, filters)
}

// Lists the library page, every library model in the order asked, narrowed to the ones the search
// page admits when the request filters
func olLibrary(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort, filters url.Values) (*v1.SearchResponse, error) {
	q := url.Values{"sort": {sort.Key}}
	query := strings.TrimSpace(req.GetQuery())
	if query != "" {
		q.Set("q", query)
	}
	page, err := c.Text(ctx, c.URL("library"), q, olPageMax)
	if err != nil {
		return nil, err
	}
	var members map[string]bool
	if len(filters) > 0 {
		if members, err = olMembers(ctx, c, filters); err != nil {
			return nil, err
		}
	}
	owner := Author(req)
	var hits []*v1.SearchHit
	for _, m := range olItemRe.FindAllStringSubmatch(page, -1) {
		hit := olHit(c, m[1])
		if hit == nil || !Matches(hit, query) || !olHasTags(hit, req.GetTags()) {
			continue
		}
		if owner != "" && !strings.EqualFold(hit.GetAuthor(), owner) || members != nil && !members[hit.GetRepo()] {
			continue
		}
		hits = append(hits, hit)
	}
	// The page holds the whole list, so ascending just flips it
	if sort.Ascending {
		slices.Reverse(hits)
	}
	return Page(hits, req, c.Limit(req)), nil
}

// Reads one page of the site's search, twenty models, which for a query goes on to community models
// after the library's. The site decides what a query matches.
func olSearch(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort, filters url.Values) (*v1.SearchResponse, error) {
	page := c.page(req)
	q := filters
	q.Set("o", sort.Key)
	q.Set("page", strconv.Itoa(page))
	if query := strings.TrimSpace(req.GetQuery()); query != "" {
		q.Set("q", query)
	}
	text, err := c.HTTP().TextWith(ctx, c.URL("search"), q, olScript, olPageMax)
	if err != nil {
		return nil, err
	}
	owner := Author(req)
	resp := &v1.SearchResponse{}
	for _, m := range olItemRe.FindAllStringSubmatch(text, -1) {
		hit := olSearchHit(c, m[1])
		if hit == nil || !olHasTags(hit, req.GetTags()) || owner != "" && !strings.EqualFold(hit.GetAuthor(), owner) {
			continue
		}
		resp.Hits = append(resp.Hits, hit)
	}
	if olMoreRe.MatchString(text) {
		resp.NextCursor = strconv.Itoa(page + 1)
	}
	return resp, nil
}

// The library models the search page lists for the filters, read to its last page
func olMembers(ctx context.Context, c *Client, filters url.Values) (map[string]bool, error) {
	memo := Cached(c, "ollama-members:"+filters.Encode(), func() *Memo[map[string]bool] { return &Memo[map[string]bool]{TTL: olMembersTTL} })
	return memo.Get(ctx, func(ctx context.Context) (map[string]bool, error) {
		q := maps.Clone(filters)
		members := map[string]bool{}
		for page := 1; page <= olMembersPages; page++ {
			q.Set("page", strconv.Itoa(page))
			text, err := c.HTTP().TextWith(ctx, c.URL("search"), q, olScript, olPageMax)
			if err != nil {
				return nil, err
			}
			for _, m := range olItemRe.FindAllStringSubmatch(text, -1) {
				if r := olResultRe.FindStringSubmatch(m[1]); r != nil {
					members[strings.TrimPrefix(r[1], olNamespace+"/")] = true
				}
			}
			if !olMoreRe.MatchString(text) {
				return members, nil
			}
		}
		return nil, fmt.Errorf("%w: ollama search went past %d pages for %s", ErrSource, olMembersPages, filters.Encode())
	})
}

// Starts a hit for a model of the library or of a community namespace
func olNewHit(c *Client, name string) *v1.SearchHit {
	hit := newHit(name, name, olAuthor(name))
	hit.Url = c.URL(namespaced(name, olNamespace))
	hit.Formats = []string{"gguf"}
	return hit
}

// Reads a listing's chips: capabilities as tags with the first as the task, the published sizes, and the cloud mark
func olChips(hit *v1.SearchHit, block string) {
	for _, chip := range olChipRe.FindAllStringSubmatch(block, -1) {
		if t := StripTags(chip[1]); t != "" && !slices.Contains(hit.Tags, t) {
			hit.Tags = append(hit.Tags, t)
		}
	}
	if len(hit.Tags) > 0 {
		hit.Task = hit.Tags[0]
		hit.Extra["capabilities"] = strings.Join(hit.Tags, ",")
	}
	var sizes []string
	for _, chip := range olSizeChipRe.FindAllStringSubmatch(block, -1) {
		if t := StripTags(chip[1]); t != "" && !slices.Contains(sizes, t) {
			sizes = append(sizes, t)
		}
	}
	if len(sizes) > 0 {
		hit.Extra["sizes"] = strings.Join(sizes, ",")
	}
	if olCloudRe.MatchString(block) {
		hit.Extra["cloud"] = "true"
	}
}

// Reads one library listing block into a hit
func olHit(c *Client, block string) *v1.SearchHit {
	m := olHrefRe.FindStringSubmatch(block)
	if m == nil {
		return nil
	}
	hit := olNewHit(c, m[1])
	if d := olDescRe.FindStringSubmatch(block); d != nil {
		hit.Description = Summary(d[1])
	}
	olChips(hit, block)
	if p := olPullsRe.FindStringSubmatch(block); p != nil {
		hit.Downloads = ParseCount(p[1])
	}
	if t := olTagsRe.FindStringSubmatch(block); t != nil {
		hit.Extra["tags"] = strconv.FormatUint(ParseCount(t[1]), 10)
	}
	if u := olUpdatedRe.FindStringSubmatch(block); u != nil {
		if ts, err := time.Parse(olUpdatedLayout, u[1]); err == nil {
			hit.UpdatedAt = timestamppb.New(ts)
		}
	}
	return hit
}

// Reads one search page result into a hit. It counts downloads exactly, dates updates to the day,
// and shows context and price for cloud models.
func olSearchHit(c *Client, block string) *v1.SearchHit {
	m := olResultRe.FindStringSubmatch(block)
	if m == nil {
		return nil
	}
	hit := olNewHit(c, strings.TrimPrefix(m[1], olNamespace+"/"))
	if d := olSummaryRe.FindStringSubmatch(block); d != nil {
		hit.Description = Summary(d[1])
	}
	olChips(hit, block)
	if n := olCountRe.FindStringSubmatch(block); n != nil {
		hit.Downloads = ParseCount(n[1])
	}
	if t := olTagCountRe.FindStringSubmatch(block); t != nil {
		hit.Extra["tags"] = strconv.FormatUint(ParseCount(t[1]), 10)
	}
	if u := olUpdatedDayRe.FindStringSubmatch(block); u != nil {
		if ts, err := time.Parse(olUpdatedDay, u[1]); err == nil {
			hit.UpdatedAt = timestamppb.New(ts)
		}
	}
	if x := olContextRe.FindStringSubmatch(block); x != nil {
		if n := ParseCount(x[1]); n > 0 {
			hit.Extra[olSortContext] = strconv.FormatUint(n, 10)
		}
	}
	if p := olPriceRe.FindStringSubmatch(block); p != nil {
		hit.Extra[olSortPrice] = p[1]
	}
	return hit
}

// Reads a model page into a hit, the same facts its library listing shows
func olPageHit(c *Client, name, page string) *v1.SearchHit {
	hit := olNewHit(c, name)
	hit.Description = Summary(olDescription(page))
	olChips(hit, page)
	if m := olDownloadsRe.FindStringSubmatch(page); m != nil {
		hit.Downloads = ParseCount(m[1])
	}
	if u := olUpdatedRe.FindStringSubmatch(page); u != nil {
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

var olTagLinkRe = regexp.MustCompile(`<a[^>]*href="/([^"]+:[^"/]+)"`)

const (
	// Manifests and config blobs read at once when a model's tags are listed
	olTagWorkers = 8
	// How long a model's tag list is kept before the registry is asked again
	olTagsTTL = 10 * time.Minute
)

// What the registry's config blob says about one tag: the weight format, the parameter count, and the quant
type olConfig struct {
	ModelFormat string `json:"model_format"`
	ModelType   string `json:"model_type"`
	FileType    string `json:"file_type"`
}

// The tag names of a model, from the library's tag page, the one listing the registry does not serve
func olTagNames(ctx context.Context, c *Client, name string) ([]string, error) {
	page, err := c.Text(ctx, c.URL(namespaced(name, olNamespace), "tags"), nil, olPageMax)
	if err != nil {
		return nil, err
	}
	// Reads each tag as a variant. Manifests provide size and digest. Configs provide parameter count
	// and quantization.
	prefix := namespaced(name, olNamespace) + ":"
	seen := map[string]bool{}
	var out []string
	for _, m := range olTagLinkRe.FindAllStringSubmatch(page, -1) {
		if !strings.HasPrefix(m[1], prefix) {
			continue
		}
		tag := strings.TrimPrefix(m[1], prefix)
		if !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out, nil
}

// Reads each tag as a variant. Manifests provide size and digest. Configs provide parameter count
// and quantization.
func (ollamaAPI) Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error) {
	name, _ := olSplit(repo, "")
	if name == "" {
		return nil, fmt.Errorf("empty repo")
	}
	memo := Cached(c, "ollama-tags:"+name, func() *Memo[[]*v1.Revision] { return &Memo[[]*v1.Revision]{TTL: olTagsTTL} })
	return memo.Get(ctx, func(ctx context.Context) ([]*v1.Revision, error) { return olTags(ctx, c, name) })
}

func olTags(ctx context.Context, c *Client, name string) ([]*v1.Revision, error) {
	tags, err := olTagNames(ctx, c, name)
	if err != nil {
		return nil, err
	}
	path := namespaced(name, olNamespace)
	def := defaultTag(tags)
	out := make([]*v1.Revision, len(tags))
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(olTagWorkers)
	for i, tag := range tags {
		eg.Go(func() error {
			m, cfg, err := olManifest(gctx, c, path, tag)
			if err != nil {
				return fmt.Errorf("%s:%s: %w", name, tag, err)
			}
			var size uint64
			for _, l := range m.Layers {
				size += uint64(l.Size)
			}
			out[i] = &v1.Revision{
				Name:       tag,
				Repo:       name + ":" + tag,
				Commit:     olCommit(m),
				Default:    tag == def,
				SizeBytes:  size,
				Parameters: olParameters(cfg.ModelType),
				Precision:  cfg.FileType,
				Detail:     strings.TrimSpace(cfg.ModelType + " " + cfg.FileType),
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	// Larger models after smaller ones, the smaller files of a size ahead of the larger
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.GetParameters() != b.GetParameters() {
			return a.GetParameters() < b.GetParameters()
		}
		if a.GetSizeBytes() != b.GetSizeBytes() {
			return a.GetSizeBytes() < b.GetSizeBytes()
		}
		return a.GetName() < b.GetName()
	})
	return out, nil
}

// One tag's manifest and the config blob it points at
func olManifest(ctx context.Context, c *Client, path, tag string) (*Manifest, olConfig, error) {
	m, err := c.Distribution().Manifest(ctx, path, tag)
	if err != nil {
		return nil, olConfig{}, err
	}
	var cfg olConfig
	if m.Config.Digest != "" {
		if err := c.Distribution().BlobJSON(ctx, path, m.Config.Digest, &cfg); err != nil {
			return nil, olConfig{}, fmt.Errorf("config: %w", err)
		}
	}
	return m, cfg, nil
}

// The manifest digest, or a hash of its layers when the registry sent none
func olCommit(m *Manifest) string {
	if m.Digest != "" {
		return m.Digest
	}
	return olDigestOf(m.Layers)
}

// A parameter count as the config blob writes it, 3.2B, 70B, or 8x7B for a mixture, zero when it says none
func olParameters(s string) uint64 {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return 0
	}
	mult := 1.0
	switch s[len(s)-1] {
	case 'B':
		mult = 1e9
	case 'M':
		mult = 1e6
	case 'K':
		mult = 1e3
	default:
		return 0
	}
	product := 1.0
	for _, part := range strings.Split(s[:len(s)-1], "X") {
		f, err := strconv.ParseFloat(part, 64)
		if err != nil || f <= 0 {
			return 0
		}
		product *= f
	}
	return uint64(product*mult + 0.5)
}

func (ollamaAPI) Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error) {
	name, tag := olSplit(repo, revision)
	if name == "" {
		return nil, fmt.Errorf("empty repo")
	}
	path := namespaced(name, olNamespace)
	m, cfg, err := olManifest(ctx, c, path, tag)
	// A bare name whose library entry publishes no latest tag resolves to the first tag it lists
	if err != nil && !tagNamed(repo, revision) && IsStatus(err, http.StatusNotFound) {
		tags, terr := olTagNames(ctx, c, name)
		if terr != nil {
			return nil, fmt.Errorf("%s:%s: %w", name, tag, err)
		}
		if def := defaultTag(tags); def != "" && def != tag {
			tag = def
			m, cfg, err = olManifest(ctx, c, path, tag)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%s:%s: %w", name, tag, err)
	}
	model := &v1.Model{Repo: name + ":" + tag, Revision: tag, Commit: olCommit(m)}
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
	card := &v1.ModelCard{Url: page, Hit: olPageHit(c, name, text)}
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
	return c.Distribution().Blob(ctx, namespaced(name, olNamespace), "sha256:"+artifact.GetSha256(), int64(artifact.GetSizeBytes()))
}
