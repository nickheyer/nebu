package sources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// GitHub: releases through its API, trees and LFS weights through git
var github = &Catalog{
	ID:   "github",
	Kind: v1.SourceKind_SOURCE_KIND_GITHUB,
	Name: "GitHub",
	Transports: []Use{
		httpUse("https://api.github.com", "GITHUB_TOKEN"),
		{Kind: TransportGit, Name: "git", Fields: map[string]string{"endpoint": "https://github.com", "token": "GITHUB_TOKEN"}},
	},
	Web:           "https://github.com",
	Description:   "GitHub, release assets and repositories with LFS weights",
	RepoExample:   "owner/repo",
	RepoPattern:   `^[\w.-]+/[\w.-]+$`,
	RevisionLabel: "release or ref",
	Sorts:         []string{SortRelevance, SortLikes, ghSortForks, SortUpdated, ghSortHelpWanted},
	// Relevance has no key, it is the API's own order. Updated orders by the last push.
	SortKeys: map[string]string{SortLikes: "stars", ghSortForks: "forks", SortUpdated: "updated", ghSortHelpWanted: "help-wanted-issues"},
	SortLabels: map[string]string{
		SortRelevance:    "Best match",
		SortLikes:        "Most stars",
		ghSortForks:      "Most forks",
		ghSortHelpWanted: "Most help wanted issues",
	},
	// The API ignores the order of its best match
	Reversible: []string{SortLikes, ghSortForks, SortUpdated, ghSortHelpWanted},
	// Qualifiers of the search query, after the licenses read from the API
	Facets: []*v1.Facet{
		Hint(NewFacet(ghFacetIn, "Search in", true, Value("name", ""), Value("description", ""), Value("topics", ""), Value("readme", "README")), "Where the query text is matched, the name, description, and topics when none is picked"),
		Freeform(FacetAuthor, "Owner"),
		Hint(Multi(Freeform(FacetTag, "Topic")), "Repositories carrying every topic named"),
		Hint(Multi(Freeform(ghFacetLanguage, "Language")), "Repositories in any language named"),
		Between(ghFacetStars, "Stars"),
		Between(ghFacetForks, "Forks"),
		Hint(Between(ghFacetSize, "Size (KB)"), "Size of the git repository in kilobytes, release assets not counted"),
		Hint(Freeform(ghFacetPushed, "Pushed"), "The last push, a date or range such as >=2025-01-01 or 2024-01-01..2024-06-30"),
		Hint(Freeform(ghFacetCreated, "Created"), "A date or range such as 2024, <2024-06, or 2024-01-01..*"),
		Hint(NewFacet(ghFacetFork, "Forked", false, Value("true", "Included"), Value("only", "Only")), "GitHub leaves forks out unless asked"),
		Toggle(ghFacetHideArchived, "Hide archived"),
		Hint(NewFacet(ghFacetVisibility, "Visibility", false, Value("public", ""), Value("private", "")), "Private repositories are the ones the token can see"),
	},
	Columns: slices.Concat(
		Shown(
			Named(Col(ColLikes), "Stars"),
			ExtraCol(ghSortForks, "Forks", AsCount, "Forks of the repository"),
			ExtraCol(ghFacetLanguage, "Language", AsText, "Main language of the repository"),
			Col(ColUpdated),
		),
		Cols(ColCreated, ColLicense),
		[]*v1.HitColumn{
			Named(Col(ColAuthor), "Owner"),
			ExtraCol("open_issues", "Open issues", AsCount, "Open issues and pull requests"),
			ExtraCol("repo_size", "Repository size", AsBytes, "Size of the git repository GitHub reports, release assets not counted"),
			ExtraCol("fork", "Fork", AsFlag, "A fork of another repository"),
			ExtraCol("archived", "Archived", AsFlag, "Archived on GitHub, read only"),
		},
	),
	API: githubAPI{},
}

func init() { register(github) }

const (
	ghSortForks      = "forks"
	ghSortHelpWanted = "help_wanted"
	ghPageSize       = 100
	ghReleaseTTL     = 5 * time.Minute
	ghShaMedia       = "application/vnd.github.sha"
	ghRawMedia       = "application/vnd.github.raw+json"
	// Results the search API serves of any query, refusing pages beyond them
	ghSearchWindow = 1000
)

// Facets only GitHub offers. The counts and dates are named as the qualifiers they fill.
const (
	ghFacetLanguage     = "language"
	ghFacetIn           = "in"
	ghFacetStars        = "stars"
	ghFacetForks        = "forks"
	ghFacetSize         = "size"
	ghFacetPushed       = "pushed"
	ghFacetCreated      = "created"
	ghFacetFork         = "fork"
	ghFacetHideArchived = "hide_archived"
	ghFacetVisibility   = "visibility"
)

// Count qualifiers, written n..m, >=n, or <=n
var ghCounts = []string{ghFacetStars, ghFacetForks, ghFacetSize}

// Date qualifiers, taking a date or time after an optional comparison, or a range with either end open as *
var ghDateQualifiers = []string{ghFacetPushed, ghFacetCreated}

// A year, month, or day, the day with an optional time and zone, as the search reads one
const ghDate = `\d{4}(-\d{2}(-\d{2}(T\d{2}:\d{2}(:\d{2})?(Z|[+-]\d{2}:\d{2})?)?)?)?`

var ghDates = regexp.MustCompile(`^(([<>]=?)?` + ghDate + `|(` + ghDate + `|\*)\.\.(` + ghDate + `|\*))$`)

// License keywords the search takes beyond the most used ones the API lists, from GitHub's licensing
// docs, named as the API names them, and each checked against the search on 2026-11-19
var ghLicenses = []*v1.FacetValue{
	Value("0bsd", "BSD Zero Clause License"),
	Value("afl-3.0", "Academic Free License v3.0"),
	Value("artistic-2.0", "Artistic License 2.0"),
	Value("bsd-3-clause-clear", "BSD 3-Clause Clear License"),
	Value("bsd-4-clause", `BSD 4-Clause "Original" or "Old" License`),
	Value("cc", "Creative Commons license family"),
	Value("cc-by-4.0", "Creative Commons Attribution 4.0 International"),
	Value("cc-by-sa-4.0", "Creative Commons Attribution Share Alike 4.0 International"),
	Value("ecl-2.0", "Educational Community License v2.0"),
	Value("epl-1.0", "Eclipse Public License 1.0"),
	Value("eupl-1.1", "European Union Public License 1.1"),
	Value("gpl", "GNU General Public License family"),
	Value("isc", "ISC License"),
	Value("lgpl", "GNU Lesser General Public License family"),
	Value("lgpl-3.0", "GNU Lesser General Public License v3.0"),
	Value("lppl-1.3c", "LaTeX Project Public License v1.3c"),
	Value("ms-pl", "Microsoft Public License"),
	Value("ncsa", "University of Illinois/NCSA Open Source License"),
	Value("ofl-1.1", "SIL Open Font License 1.1"),
	Value("osl-3.0", "Open Software License 3.0"),
	Value("other", "Other"),
	Value("postgresql", "PostgreSQL License"),
	Value("wtfpl", "Do What The F*ck You Want To Public License"),
	Value("zlib", "zlib License"),
}

type ghAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt string    `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

// One branch or tag as the API lists them
type ghRef struct {
	Name   string `json:"name"`
	Commit struct {
		Sha string `json:"sha"`
	} `json:"commit"`
}

// Caches repository refs and one release page to limit API requests.
type ghRepoState struct {
	owner, name   string
	defaultBranch string
	// The repository as the API lists it
	item ghItem
	// Lists recent releases, the latest stable release, branches, and tags without releases.
	releases []ghRelease
	mu       sync.Mutex
	// Releases asked for by tag beyond that page, nil for a tag that names no release
	byTag map[string]*ghRelease
	// The stable release beyond that page, once looked for
	stable       *ghRelease
	stableLooked bool
	// Caches the first refs page. Fetches the default branch separately if it falls outside the
	// alphabetical page.
	pages map[string][]ghRef
}

// The GitHub API for search, releases, refs, and cards, git for trees and blobs
type githubAPI struct{}

type ghItem struct {
	FullName      string   `json:"full_name"`
	DefaultBranch string   `json:"default_branch"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	HTMLURL       string   `json:"html_url"`
	Stars         uint64   `json:"stargazers_count"`
	Forks         uint64   `json:"forks_count"`
	OpenIssues    uint64   `json:"open_issues_count"`
	Language      string   `json:"language"`
	Topics        []string `json:"topics"`
	PushedAt      string   `json:"pushed_at"`
	CreatedAt     string   `json:"created_at"`
	Private       bool     `json:"private"`
	Fork          bool     `json:"fork"`
	Archived      bool     `json:"archived"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
	License *struct {
		SPDX string `json:"spdx_id"`
	} `json:"license"`
	// Kilobytes of git storage
	Size uint64 `json:"size"`
}

// The licenses GitHub lists as most used, then the rest its search takes, by name
func (githubAPI) Facets(ctx context.Context, c *Client) ([]*v1.Facet, error) {
	var listed []struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	if _, err := c.JSON(ctx, c.URL("licenses"), url.Values{"per_page": {"100"}}, &listed); err != nil {
		return nil, err
	}
	values := make([]*v1.FacetValue, 0, len(listed)+len(ghLicenses))
	for _, l := range listed {
		values = append(values, Value(l.Key, l.Name))
	}
	for _, v := range ghLicenses {
		if !slices.ContainsFunc(values, func(have *v1.FacetValue) bool { return have.GetId() == v.GetId() }) {
			values = append(values, v)
		}
	}
	slices.SortFunc(values, func(a, b *v1.FacetValue) int {
		return strings.Compare(strings.ToLower(a.GetLabel()), strings.ToLower(b.GetLabel()))
	})
	return []*v1.Facet{Hint(NewFacet(FacetLicense, "License", true, values...), "Other is a license GitHub matches to none it knows")}, nil
}

// Builds the search query: the text, then a qualifier for each filter asked. GitHub drops a
// qualifier value it cannot read and answers unfiltered, so the picked ones are checked here.
func ghQuery(req *v1.SearchRequest) (string, error) {
	var terms []string
	if owner := Author(req); owner != "" {
		terms = append(terms, ghQualifier("user", owner))
	}
	for _, t := range append(Filter(req, FacetTag), req.GetTags()...) {
		terms = append(terms, ghQualifier("topic", strings.TrimSpace(t)))
	}
	for _, lang := range Filter(req, ghFacetLanguage) {
		terms = append(terms, ghQualifier("language", lang))
	}
	// Repeated licenses match any of them, and GitHub refuses one it does not know
	for _, l := range Filter(req, FacetLicense) {
		terms = append(terms, ghQualifier("license", l))
	}
	for _, id := range ghCounts {
		bounds, err := Range(req, id)
		if err != nil {
			return "", err
		}
		if n := ghCount(bounds); n != "" {
			terms = append(terms, id+":"+n)
		}
	}
	for _, id := range ghDateQualifiers {
		if d := FilterOne(req, id); d != "" {
			if !ghDates.MatchString(d) {
				return "", fmt.Errorf("%w: %s %q is no date or range, want one such as >=2025-01-01 or 2024-01-01..2024-12-31", ErrSource, id, d)
			}
			terms = append(terms, id+":"+d)
		}
	}
	if On(req, ghFacetHideArchived) {
		terms = append(terms, "archived:false")
	}
	in, fork, visibility := Filter(req, ghFacetIn), FilterOne(req, ghFacetFork), FilterOne(req, ghFacetVisibility)
	if err := errors.Join(ghOffered(ghFacetIn, in...), ghOffered(ghFacetFork, fork), ghOffered(ghFacetVisibility, visibility)); err != nil {
		return "", err
	}
	if visibility != "" {
		terms = append(terms, "is:"+visibility)
	}
	// The API refuses a query of only in and fork, so browsing lists everything with a star
	query := strings.TrimSpace(req.GetQuery())
	switch {
	case query != "":
		terms = append([]string{query}, terms...)
		if len(in) > 0 {
			terms = append(terms, "in:"+strings.Join(in, ","))
		}
	case len(terms) == 0:
		terms = append(terms, "stars:>0")
	}
	if fork != "" {
		terms = append(terms, "fork:"+fork)
	}
	return strings.Join(terms, " "), nil
}

// Refuses a value the pick list does not offer, blank standing for none
func ghOffered(id string, values ...string) error {
	i := slices.IndexFunc(github.Facets, func(f *v1.Facet) bool { return f.GetId() == id })
	for _, v := range values {
		if v != "" && !slices.ContainsFunc(github.Facets[i].GetValues(), func(o *v1.FacetValue) bool { return o.GetId() == v }) {
			return fmt.Errorf("%w: %s %q is not one GitHub reads", ErrSource, id, v)
		}
	}
	return nil
}

// A qualifier with its value, quoted when the value holds a space as Jupyter Notebook does
func ghQualifier(name, value string) string {
	if strings.ContainsAny(value, " \t") {
		return name + `:"` + value + `"`
	}
	return name + ":" + value
}

// Writes a count range as GitHub reads it, empty when both sides are open
func ghCount(b Bounds) string {
	switch {
	case b.Min != nil && b.Max != nil:
		return strconv.FormatUint(*b.Min, 10) + ".." + strconv.FormatUint(*b.Max, 10)
	case b.Min != nil:
		return ">=" + strconv.FormatUint(*b.Min, 10)
	case b.Max != nil:
		return "<=" + strconv.FormatUint(*b.Max, 10)
	}
	return ""
}

func (githubAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	query, err := ghQuery(req)
	if err != nil {
		return nil, err
	}
	limit, page := c.Limit(req), c.page(req)
	q := url.Values{"q": {query}, "per_page": {strconv.Itoa(limit)}, "page": {strconv.Itoa(page)}}
	if sort.Key != "" {
		q.Set("sort", sort.Key)
		q.Set("order", direction(sort.Ascending, "asc", "desc"))
	}
	var body struct {
		TotalCount uint64   `json:"total_count"`
		Items      []ghItem `json:"items"`
	}
	if _, err := c.JSON(ctx, c.URL("search", "repositories"), q, &body); err != nil {
		return nil, err
	}
	resp := &v1.SearchResponse{Total: body.TotalCount}
	for _, it := range body.Items {
		resp.Hits = append(resp.Hits, ghHit(it))
	}
	c.nextPage(resp, page, limit, min(body.TotalCount, ghSearchWindow))
	return resp, nil
}

func ghHit(it ghItem) *v1.SearchHit {
	hit := newHit(it.FullName, it.Name, it.Owner.Login)
	hit.Likes, hit.Description, hit.Url = it.Stars, Excerpt(it.Description, summaryLen), it.HTMLURL
	hit.Private, hit.Tags = it.Private, it.Topics
	hit.UpdatedAt, hit.CreatedAt = Stamp(it.PushedAt), Stamp(it.CreatedAt)
	if it.License != nil && it.License.SPDX != "" && it.License.SPDX != "NOASSERTION" {
		hit.License = it.License.SPDX
	}
	if it.Language != "" {
		hit.Extra[ghFacetLanguage] = it.Language
	}
	if it.Forks > 0 {
		hit.Extra[ghSortForks] = strconv.FormatUint(it.Forks, 10)
	}
	if it.OpenIssues > 0 {
		hit.Extra["open_issues"] = strconv.FormatUint(it.OpenIssues, 10)
	}
	if it.Size > 0 {
		hit.Extra["repo_size"] = strconv.FormatUint(it.Size*1024, 10)
	}
	if it.Fork {
		hit.Extra["fork"] = "true"
	}
	if it.Archived {
		hit.Extra["archived"] = "true"
	}
	return hit
}

// Splits owner/name, refusing anything else
func ghSplit(repo string) (string, string, error) {
	parts, err := segments("repo", repo, "owner/name", 2)
	if err != nil {
		return "", "", err
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

// Caches the first refs page. Fetches the default branch separately if it falls outside the
// alphabetical page.
func ghListURL(c *Client, owner, name, kind string) string {
	return c.URL("repos", owner, name, kind) + "?per_page=" + strconv.Itoa(ghPageSize)
}

// Reads the default branch and the newest page of releases of a repo, kept a while per source
func ghState(ctx context.Context, c *Client, repo string) (*ghRepoState, error) {
	owner, name, err := ghSplit(repo)
	if err != nil {
		return nil, err
	}
	memo := Cached(c, "github:"+owner+"/"+name, func() *Memo[*ghRepoState] { return &Memo[*ghRepoState]{TTL: ghReleaseTTL} })
	return memo.Get(ctx, func(ctx context.Context) (*ghRepoState, error) {
		st := &ghRepoState{owner: owner, name: name, byTag: map[string]*ghRelease{}, pages: map[string][]ghRef{}}
		if _, err := c.JSON(ctx, c.URL("repos", owner, name), nil, &st.item); err != nil {
			return nil, err
		}
		st.defaultBranch = st.item.DefaultBranch
		// One page: a repository that releases every commit has thousands, and an older one resolves by its tag
		if _, err := c.JSON(ctx, ghListURL(c, owner, name, "releases"), nil, &st.releases); err != nil {
			return nil, err
		}
		return st, nil
	})
}

// The newest release that is neither draft nor prerelease, else the newest of any kind, nil when there are none
func (st *ghRepoState) latest(ctx context.Context, c *Client) (*ghRelease, error) {
	for i := range st.releases {
		if r := &st.releases[i]; !r.Draft && !r.Prerelease {
			return r, nil
		}
	}
	if len(st.releases) == 0 {
		return nil, nil
	}
	if len(st.releases) < ghPageSize {
		return &st.releases[0], nil
	}
	// A whole page without a stable release means it is older, and the API names it directly
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.stableLooked {
		var rel ghRelease
		if _, err := c.JSON(ctx, c.URL("repos", st.owner, st.name, "releases", "latest"), nil, &rel); err != nil {
			if !IsStatus(err, http.StatusNotFound) {
				return nil, err
			}
		} else {
			st.stable = &rel
			st.byTag[rel.TagName] = &rel
		}
		st.stableLooked = true
	}
	if st.stable != nil {
		return st.stable, nil
	}
	return &st.releases[0], nil
}

// Finds a cached release by tag, or fetches and caches it. Returns nil for non-release tags.
func (st *ghRepoState) release(ctx context.Context, c *Client, tag string) (*ghRelease, error) {
	if tag == "" {
		return nil, nil
	}
	for i := range st.releases {
		if st.releases[i].TagName == tag {
			return &st.releases[i], nil
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if rel, ok := st.byTag[tag]; ok {
		return rel, nil
	}
	var rel ghRelease
	if _, err := c.JSON(ctx, c.URL("repos", st.owner, st.name, "releases", "tags", tag), nil, &rel); err != nil {
		if !IsStatus(err, http.StatusNotFound) {
			return nil, err
		}
		st.byTag[tag] = nil
		return nil, nil
	}
	st.byTag[tag] = &rel
	return &rel, nil
}

// Caches the first refs page. Fetches the default branch separately if it falls outside the
// alphabetical page.
func (st *ghRepoState) refs(ctx context.Context, c *Client, kind string) ([]ghRef, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if refs, ok := st.pages[kind]; ok {
		return refs, nil
	}
	var refs []ghRef
	if _, err := c.JSON(ctx, ghListURL(c, st.owner, st.name, kind), nil, &refs); err != nil {
		return nil, err
	}
	if kind == "branches" {
		i := slices.IndexFunc(refs, func(r ghRef) bool { return r.Name == st.defaultBranch })
		switch {
		case i > 0:
			head := refs[i]
			refs = append([]ghRef{head}, slices.Delete(refs, i, i+1)...)
		case i < 0 && len(refs) == ghPageSize:
			sha, err := ghCommit(ctx, c, st.owner, st.name, st.defaultBranch)
			if err != nil {
				return nil, err
			}
			head := ghRef{Name: st.defaultBranch}
			head.Commit.Sha = sha
			refs = append([]ghRef{head}, refs...)
		}
	}
	st.pages[kind] = refs
	return refs, nil
}

// Reads the commit a ref names
func ghCommit(ctx context.Context, c *Client, owner, name, ref string) (string, error) {
	sha, err := c.HTTP().TextWith(ctx, c.URL("repos", owner, name, "commits", ref), nil, http.Header{"Accept": {ghShaMedia}}, 128)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

// The commit a tag, branch, or abbreviated hash names
func (githubAPI) Commit(ctx context.Context, c *Client, repo, ref string) (string, error) {
	owner, name, err := ghSplit(repo)
	if err != nil {
		return "", err
	}
	return ghCommit(ctx, c, owner, name, ref)
}

// A release resolves to its assets, any other ref to the tree at that commit
func (githubAPI) Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error) {
	owner, name, err := ghSplit(repo)
	if err != nil {
		return nil, err
	}
	st, err := ghState(ctx, c, repo)
	if err != nil {
		return nil, err
	}
	rel, err := st.release(ctx, c, revision)
	if err != nil {
		return nil, err
	}
	if revision == "" {
		if rel, err = st.latest(ctx, c); err != nil {
			return nil, err
		}
		if rel != nil {
			revision = rel.TagName
		} else {
			revision = st.defaultBranch
		}
	}
	commit, err := ghCommit(ctx, c, owner, name, revision)
	if err != nil {
		return nil, err
	}
	model := &v1.Model{Repo: owner + "/" + name, Revision: revision, Commit: commit}
	if rel != nil {
		for _, as := range rel.Assets {
			model.Artifacts = append(model.Artifacts, &v1.Artifact{Path: as.Name, SizeBytes: uint64(as.Size), Sha256: Hex(as.Digest), Url: as.URL})
		}
		return model, nil
	}
	if model.Artifacts, err = c.Git().List(ctx, owner+"/"+name+"@"+commit); err != nil {
		return nil, err
	}
	return model, nil
}

// Lists recent releases, the latest stable release, branches, and tags without releases.
func (githubAPI) Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error) {
	st, err := ghState(ctx, c, repo)
	if err != nil {
		return nil, err
	}
	latest, err := st.latest(ctx, c)
	if err != nil {
		return nil, err
	}
	var out []*v1.Revision
	seen := map[string]bool{}
	release := func(r *ghRelease) {
		detail := "release"
		switch {
		case r.Draft:
			detail = "draft"
		case r.Prerelease:
			detail = "prerelease"
		}
		if r.Name != "" && r.Name != r.TagName {
			detail += ", " + r.Name
		}
		rev := &v1.Revision{Name: r.TagName, Default: latest != nil && r.TagName == latest.TagName, Detail: detail, UpdatedAt: Stamp(r.PublishedAt)}
		for _, as := range r.Assets {
			rev.SizeBytes += uint64(as.Size)
		}
		out = append(out, rev)
		seen[r.TagName] = true
	}
	for i := range st.releases {
		release(&st.releases[i])
	}
	if latest != nil && !seen[latest.TagName] {
		release(latest)
	}
	for _, kind := range []string{"branches", "tags"} {
		refs, err := st.refs(ctx, c, kind)
		if err != nil {
			return nil, err
		}
		for _, e := range refs {
			if seen[e.Name] {
				continue
			}
			seen[e.Name] = true
			out = append(out, refRevision(e.Name, e.Commit.Sha, latest == nil && kind == "branches" && e.Name == st.defaultBranch, kind == "tags"))
		}
	}
	return out, nil
}

func (githubAPI) Card(ctx context.Context, c *Client, repo, revision string) (*v1.ModelCard, error) {
	owner, name, err := ghSplit(repo)
	if err != nil {
		return nil, err
	}
	page := c.Page(owner, name)
	var q url.Values
	if revision != "" {
		q = url.Values{"ref": {revision}}
	}
	st, err := ghState(ctx, c, repo)
	if err != nil {
		return nil, err
	}
	text, err := c.HTTP().TextWith(ctx, c.URL("repos", owner, name, "readme"), q, http.Header{"Accept": {ghRawMedia}}, cardMax)
	if err != nil {
		card, err := cardOrEmpty(page, err)
		if err != nil {
			return nil, err
		}
		card.Hit = ghHit(st.item)
		return card, nil
	}
	return &v1.ModelCard{Markdown: text, Url: page, Hit: ghHit(st.item)}, nil
}

// A release asset downloads from its link, a tree file comes through git
func (githubAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	st, err := ghState(ctx, c, model.GetRepo())
	if err != nil {
		return nil, err
	}
	rel, err := st.release(ctx, c, model.GetRevision())
	if err != nil {
		return nil, err
	}
	if rel != nil {
		for _, as := range rel.Assets {
			if as.Name == artifact.GetPath() {
				return c.HTTP().Open(ctx, as.URL, as.Size)
			}
		}
		return nil, fmt.Errorf("%s: release %s has no asset %s", model.GetRepo(), model.GetRevision(), artifact.GetPath())
	}
	ref := model.GetCommit()
	if ref == "" {
		ref = model.GetRevision()
	}
	return c.Git().Open(ctx, model.GetRepo()+"@"+ref+"/"+artifact.GetPath(), int64(artifact.GetSizeBytes()))
}
