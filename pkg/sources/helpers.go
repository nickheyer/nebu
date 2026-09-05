package sources

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Sort ids every provider maps onto its own ordering
const (
	SortRelevance = "relevance"
	SortTrending  = "trending"
	SortDownloads = "downloads"
	SortLikes     = "likes"
	SortUpdated   = "updated"
	SortCreated   = "created"
	SortName      = "name"
)

// Facet ids shared across providers so a client can treat them alike
const (
	FacetTask       = "task"
	FacetLibrary    = "library"
	FacetLicense    = "license"
	FacetType       = "type"
	FacetPeriod     = "period"
	FacetCapability = "capability"
	FacetFramework  = "framework"
	FacetBaseModel  = "base_model"
	FacetPublisher  = "publisher"
	FacetNSFW       = "nsfw"
	FacetAuthor     = "author"
	FacetTag        = "tag"
)

const (
	// The tag a registry serves when a repo names none
	latestTag = "latest"
	// Runes a description is cut to
	summaryLen = 240
)

var sortLabels = map[string]string{
	SortRelevance: "Relevance",
	SortTrending:  "Trending",
	SortDownloads: "Most downloaded",
	SortLikes:     "Most liked",
	SortUpdated:   "Recently updated",
	SortCreated:   "Newest",
	SortName:      "Name",
}

// Builds sort options with the shared labels
func Sorts(ids ...string) []*v1.SortOption {
	out := make([]*v1.SortOption, 0, len(ids))
	for _, id := range ids {
		label := sortLabels[id]
		if label == "" {
			label = Humanize(id)
		}
		out = append(out, &v1.SortOption{Id: id, Label: label})
	}
	return out
}

// Builds one facet value
func Value(id, label string) *v1.FacetValue {
	if label == "" {
		label = Humanize(id)
	}
	return &v1.FacetValue{Id: id, Label: label}
}

// Builds one facet value inside a group
func GroupedValue(id, label, group string) *v1.FacetValue {
	v := Value(id, label)
	v.Group = group
	return v
}

// Builds a facet with fixed values
func NewFacet(id, label string, multi bool, values ...*v1.FacetValue) *v1.Facet {
	return &v1.Facet{Id: id, Label: label, Values: values, Multi: multi}
}

// Builds a facet the person types a value into
func Freeform(id, label string) *v1.Facet {
	return &v1.Facet{Id: id, Label: label, Freeform: true}
}

// One facet a provider reads from its own tag taxonomy
type taxonomy struct{ kind, id, label string }

// Builds a single choice facet per taxonomy from the values fetch reads for its kind
func taxonomyFacets(ctx context.Context, specs []taxonomy, fetch func(ctx context.Context, kind string) ([]*v1.FacetValue, error)) ([]*v1.Facet, error) {
	var out []*v1.Facet
	for _, t := range specs {
		values, err := fetch(ctx, t.kind)
		if err != nil {
			return nil, err
		}
		out = append(out, NewFacet(t.id, t.label, false, values...))
	}
	return out, nil
}

// Turns an identifier like text-generation into Text generation
func Humanize(id string) string {
	s := strings.NewReplacer("-", " ", "_", " ").Replace(id)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Returns the values asked for one facet, split on commas
func Filter(req *v1.SearchRequest, facet string) []string {
	raw, ok := req.GetFilters()[facet]
	if !ok {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Returns the first value asked for one facet
func FilterOne(req *v1.SearchRequest, facet string) string {
	if vs := Filter(req, facet); len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// Returns the author filter from either field
func Author(req *v1.SearchRequest) string {
	if a := strings.TrimSpace(req.GetAuthor()); a != "" {
		return a
	}
	return FilterOne(req, FacetAuthor)
}

// Clamps the requested page size
func Limit(req *v1.SearchRequest, def, max int) int {
	n := int(req.GetLimit())
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// Reads an offset cursor, zero when blank or malformed
func Offset(cursor string) int {
	n, err := strconv.Atoi(strings.TrimSpace(cursor))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// Slices an in memory result set by the request's offset cursor and a page size
func Page(hits []*v1.SearchHit, req *v1.SearchRequest, limit int) *v1.SearchResponse {
	start := Offset(req.GetCursor())
	if start > len(hits) {
		start = len(hits)
	}
	end := start + limit
	if end > len(hits) {
		end = len(hits)
	}
	resp := &v1.SearchResponse{Hits: hits[start:end], Total: uint64(len(hits))}
	if end < len(hits) {
		resp.NextCursor = strconv.Itoa(end)
	}
	return resp
}

// Keeps the hits the request's author and query admit, ordered and paged
func localPage(hits []*v1.SearchHit, req *v1.SearchRequest, order Sort, limit int) *v1.SearchResponse {
	author := Author(req)
	kept := hits[:0]
	for _, hit := range hits {
		if author != "" && !strings.EqualFold(author, hit.GetAuthor()) {
			continue
		}
		if Matches(hit, req.GetQuery()) {
			kept = append(kept, hit)
		}
	}
	SortHits(kept, order.ID, order.Ascending)
	return Page(kept, req, limit)
}

// Reports whether every word of the query appears in the hit
func Matches(hit *v1.SearchHit, query string) bool {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return true
	}
	hay := strings.ToLower(strings.Join(append([]string{hit.GetRepo(), hit.GetName(), hit.GetAuthor(), hit.GetDescription(), hit.GetTask()}, hit.GetTags()...), "\n"))
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// Orders hits in place by a shared sort id, stable on the original order
func SortHits(hits []*v1.SearchHit, id string, ascending bool) {
	less := func(a, b *v1.SearchHit) bool {
		switch id {
		case SortDownloads:
			return a.GetDownloads() > b.GetDownloads()
		case SortLikes:
			return a.GetLikes() > b.GetLikes()
		case SortUpdated:
			return stamp(a.GetUpdatedAt()).After(stamp(b.GetUpdatedAt()))
		case SortCreated:
			return stamp(a.GetCreatedAt()).After(stamp(b.GetCreatedAt()))
		case SortName:
			return strings.ToLower(a.GetRepo()) < strings.ToLower(b.GetRepo())
		}
		return false
	}
	if id == "" || id == SortRelevance {
		return
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if ascending {
			return less(hits[j], hits[i])
		}
		return less(hits[i], hits[j])
	})
}

func stamp(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

var stampLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999", "2006-01-02"}

// Parses the timestamps catalogs print, zone and fraction optional, nil when blank or odd
func Stamp(s string) *timestamppb.Timestamp {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range stampLayouts {
		if t, err := time.Parse(layout, s); err == nil && !t.IsZero() {
			return timestamppb.New(t)
		}
	}
	return nil
}

// Starts a hit with room for the extras a provider adds
func newHit(repo, name, author string) *v1.SearchHit {
	return &v1.SearchHit{Repo: repo, Name: name, Author: author, Extra: map[string]string{}}
}

// Builds a branch or tag revision, its detail naming which
func refRevision(name, commit string, isDefault, tag bool) *v1.Revision {
	detail := "branch"
	if tag {
		detail = "tag"
	}
	return &v1.Revision{Name: name, Commit: commit, Default: isDefault, Detail: detail}
}

// An empty card at page when the source has nothing there, the error otherwise
func cardOrEmpty(page string, err error) (*v1.ModelCard, error) {
	if IsStatus(err, http.StatusNotFound) {
		return &v1.ModelCard{Url: page}, nil
	}
	return nil, err
}

// Picks the word for a sort direction
func direction(ascending bool, up, down string) string {
	if ascending {
		return up
	}
	return down
}

// The error for an artifact whose size or digest nobody reported
func unknown(what, field string) error {
	return fmt.Errorf("%s: unknown %s", what, field)
}

// Splits author/name, an author of none when the repo has no slash
func splitRepo(repo string) (author, name string) {
	author, name, _ = strings.Cut(repo, "/")
	if name == "" {
		return "", repo
	}
	return author, name
}

// Reports whether the repo names a variant by tag, splitting name:tag
func SplitTag(repo, def string) (string, string) {
	if i := strings.LastIndex(repo, ":"); i > 0 && !strings.Contains(repo[i+1:], "/") {
		return repo[:i], repo[i+1:]
	}
	return repo, def
}

// Splits name:tag, an explicit revision winning and def standing in for none
func splitRef(repo, revision, def string) (name, tag string) {
	name, tag = SplitTag(strings.TrimSpace(repo), "")
	if revision != "" {
		tag = revision
	}
	if tag == "" {
		tag = def
	}
	return name, tag
}

// Puts a bare name under a namespace
func namespaced(name, ns string) string {
	if strings.Contains(name, "/") {
		return name
	}
	return ns + "/" + name
}

// Splits repo@revision/path into its parts
func splitRepoRef(locator string) (repo, ref, rest string) {
	repo, tail, ok := strings.Cut(locator, "@")
	if !ok {
		return locator, "", ""
	}
	ref, rest, _ = strings.Cut(tail, "/")
	return repo, ref, rest
}

// Splits a slash separated locator, refusing empty pieces and counts not in want
func segments(what, s, shape string, want ...int) ([]string, error) {
	parts := strings.Split(strings.Trim(strings.TrimSpace(s), "/"), "/")
	if !slices.Contains(want, len(parts)) {
		return nil, fmt.Errorf("%s %q: want %s", what, s, shape)
	}
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("%s %q: empty segment", what, s)
		}
	}
	return parts, nil
}

// Strips the sha256: prefix from a digest, lower cased
func Hex(digest string) string {
	return strings.ToLower(strings.TrimPrefix(digest, "sha256:"))
}

// Counts names handed out so a repeat gets a numbered suffix before its extension
type namer map[string]int

// Returns name the first time, name-2 and up on repeats
func (n namer) unique(name string) string {
	n[name]++
	if count := n[name]; count > 1 {
		return withSuffix(name, fmt.Sprintf("-%d", count))
	}
	return name
}

// Inserts a suffix before the file extension
func withSuffix(name, suffix string) string {
	ext := path.Ext(name)
	return strings.TrimSuffix(name, ext) + suffix + ext
}

// Caches one value for a while, refilling on demand
type Memo[T any] struct {
	TTL time.Duration
	mu  sync.Mutex
	at  time.Time
	val T
	ok  bool
}

// Returns the cached value or fills it, keeping a stale value when filling fails
func (m *Memo[T]) Get(ctx context.Context, fill func(ctx context.Context) (T, error)) (T, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ok && (m.TTL <= 0 || time.Since(m.at) < m.TTL) {
		return m.val, nil
	}
	val, err := fill(ctx)
	if err != nil {
		if m.ok {
			return m.val, nil
		}
		return val, err
	}
	m.val, m.ok, m.at = val, true, time.Now()
	return val, nil
}

var (
	tagRe   = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe = regexp.MustCompile(`\s+`)
	dropRe  = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</\s*(script|style)\s*>`)
)

// Turns HTML into plain text, collapsing whitespace
func StripTags(s string) string {
	s = dropRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// Shortens text to at most n runes on a word boundary
func Excerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	if i := strings.LastIndexAny(cut, " \n\t"); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

// Shortens marked up text to a one line description
func Summary(s string) string {
	return Excerpt(StripTags(s), summaryLen)
}

// Parses a compact count such as 1.4M or 12K into a number
func ParseCount(s string) uint64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return 0
	}
	mult := 1.0
	switch s[len(s)-1] {
	case 'K', 'k':
		mult, s = 1e3, s[:len(s)-1]
	case 'M', 'm':
		mult, s = 1e6, s[:len(s)-1]
	case 'B', 'b':
		mult, s = 1e9, s[:len(s)-1]
	}
	var f float64
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			return 0
		}
	}
	if _, err := fmt.Sscan(s, &f); err != nil {
		return 0
	}
	return uint64(f*mult + 0.5)
}

// Parses a human size such as 1.3GB into bytes, decimal units as sites print them
func ParseSize(s string) uint64 {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		mult   float64
	}{{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"B", 1}}
	upper := strings.ToUpper(s)
	for _, u := range units {
		if strings.HasSuffix(upper, u.suffix) {
			var f float64
			if _, err := fmt.Sscan(strings.TrimSpace(upper[:len(upper)-len(u.suffix)]), &f); err != nil {
				return 0
			}
			return uint64(f*u.mult + 0.5)
		}
	}
	return 0
}
