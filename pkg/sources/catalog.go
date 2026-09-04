package sources

import (
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Sort ids every source maps onto its own ordering
const (
	SortRelevance = "relevance"
	SortTrending  = "trending"
	SortDownloads = "downloads"
	SortLikes     = "likes"
	SortUpdated   = "updated"
	SortCreated   = "created"
	SortName      = "name"
	SortSize      = "size"
)

// Facet ids shared across sources so a client can treat them alike
const (
	FacetTask       = "task"
	FacetLibrary    = "library"
	FacetLicense    = "license"
	FacetFormat     = "format"
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

var sortLabels = map[string]string{
	SortRelevance: "Relevance",
	SortTrending:  "Trending",
	SortDownloads: "Most downloaded",
	SortLikes:     "Most liked",
	SortUpdated:   "Recently updated",
	SortCreated:   "Newest",
	SortName:      "Name",
	SortSize:      "Size",
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
		case SortSize:
			return a.GetSizeBytes() > b.GetSizeBytes()
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

func stamp(ts interface{ AsTime() time.Time }) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

var stampLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999", "2006-01-02"}

// Parses the timestamps catalogs print, with or without a zone or fractional seconds, nil when blank or odd
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

// Reports whether the repo names a variant by tag, splitting name:tag
func SplitTag(repo, def string) (string, string) {
	if i := strings.LastIndex(repo, ":"); i > 0 && !strings.Contains(repo[i+1:], "/") {
		return repo[:i], repo[i+1:]
	}
	return repo, def
}
