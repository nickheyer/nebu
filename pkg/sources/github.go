package sources

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
		{Kind: TransportGit, Name: "git", Fields: map[string]string{"endpoint": "https://github.com", "token_env": "GITHUB_TOKEN"}},
	},
	Web:           "https://github.com",
	Description:   "GitHub, release assets and repositories with LFS weights",
	RepoExample:   "owner/repo",
	RepoPattern:   `^[\w.-]+/[\w.-]+$`,
	RevisionLabel: "release or ref",
	Sorts:         []string{SortRelevance, SortLikes, SortUpdated, ghSortForks},
	// Relevance has no key, it is the API's own order
	SortKeys:   map[string]string{SortLikes: "stars", SortUpdated: "updated", ghSortForks: "forks"},
	Reversible: []string{SortLikes, SortUpdated, ghSortForks},
	Facets: []*v1.Facet{
		Freeform(FacetAuthor, "Owner"),
		Freeform(FacetTag, "Topic"),
		Freeform(ghFacetLanguage, "Language"),
	},
	HitFields: []*v1.ConfigField{
		{Name: ghFacetLanguage, Label: "Language", Description: "Main language of the repository"},
		{Name: "archived", Label: "Archived", Type: v1.ConfigType_CONFIG_TYPE_BOOL, Description: "Archived on GitHub, read only"},
	},
	API: githubAPI{},
}

func init() { register(github) }

const (
	ghSortForks     = "forks"
	ghFacetLanguage = "language"
	ghPageSize      = 100
	ghReleaseTTL    = 5 * time.Minute
	ghShaMedia      = "application/vnd.github.sha"
	ghRawMedia      = "application/vnd.github.raw+json"
)

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

// Releases and the default branch of one repository
type ghRepoState struct {
	releases      []ghRelease
	defaultBranch string
}

// The GitHub API for search, releases, refs, and cards, git for trees and blobs
type githubAPI struct{}

type ghItem struct {
	FullName    string   `json:"full_name"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	HTMLURL     string   `json:"html_url"`
	Stars       uint64   `json:"stargazers_count"`
	Forks       uint64   `json:"forks_count"`
	Language    string   `json:"language"`
	Topics      []string `json:"topics"`
	PushedAt    string   `json:"pushed_at"`
	CreatedAt   string   `json:"created_at"`
	Private     bool     `json:"private"`
	Archived    bool     `json:"archived"`
	Owner       struct {
		Login string `json:"login"`
	} `json:"owner"`
	License *struct {
		SPDX string `json:"spdx_id"`
	} `json:"license"`
}

func (githubAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	terms := []string{}
	if query := strings.TrimSpace(req.GetQuery()); query != "" {
		terms = append(terms, query)
	}
	if owner := Author(req); owner != "" {
		terms = append(terms, "user:"+owner)
	}
	for _, t := range append(Filter(req, FacetTag), req.GetTags()...) {
		terms = append(terms, "topic:"+strings.TrimSpace(t))
	}
	if lang := FilterOne(req, ghFacetLanguage); lang != "" {
		terms = append(terms, "language:"+lang)
	}
	// The search API refuses an empty query, so browsing lists everything with a star
	if len(terms) == 0 {
		terms = append(terms, "stars:>0")
	}
	limit, page := c.Limit(req), c.page(req)
	q := url.Values{"q": {strings.Join(terms, " ")}, "per_page": {strconv.Itoa(limit)}, "page": {strconv.Itoa(page)}}
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
	c.nextPage(resp, page, limit, body.TotalCount)
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

// Reads the releases and default branch of a repo, kept a while per source
func ghState(ctx context.Context, c *Client, repo string) (*ghRepoState, error) {
	owner, name, err := ghSplit(repo)
	if err != nil {
		return nil, err
	}
	memo := Cached(c, "github:"+owner+"/"+name, func() *Memo[*ghRepoState] { return &Memo[*ghRepoState]{TTL: ghReleaseTTL} })
	return memo.Get(ctx, func(ctx context.Context) (*ghRepoState, error) {
		st := &ghRepoState{}
		var info struct {
			DefaultBranch string `json:"default_branch"`
		}
		if _, err := c.JSON(ctx, c.URL("repos", owner, name), nil, &info); err != nil {
			return nil, err
		}
		st.defaultBranch = info.DefaultBranch
		next := c.URL("repos", owner, name, "releases") + "?per_page=" + strconv.Itoa(ghPageSize)
		if err := eachPage(ctx, c, next, nil, func(page []ghRelease) { st.releases = append(st.releases, page...) }); err != nil {
			return nil, err
		}
		return st, nil
	})
}

// The newest release that is neither draft nor prerelease, else the newest of any kind
func (st *ghRepoState) latest() *ghRelease {
	for i := range st.releases {
		if r := &st.releases[i]; !r.Draft && !r.Prerelease {
			return r
		}
	}
	if len(st.releases) > 0 {
		return &st.releases[0]
	}
	return nil
}

func (st *ghRepoState) release(tag string) *ghRelease {
	for i := range st.releases {
		if st.releases[i].TagName == tag {
			return &st.releases[i]
		}
	}
	return nil
}

// Reads the commit a ref names
func ghCommit(ctx context.Context, c *Client, owner, name, ref string) (string, error) {
	sha, err := c.HTTP().TextWith(ctx, c.URL("repos", owner, name, "commits", ref), nil, http.Header{"Accept": {ghShaMedia}}, 128)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
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
	rel := st.release(revision)
	if revision == "" {
		if rel = st.latest(); rel != nil {
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
			model.Artifacts = append(model.Artifacts, &v1.Artifact{Path: as.Name, SizeBytes: uint64(as.Size), Sha256: Hex(as.Digest)})
		}
		return model, nil
	}
	if model.Artifacts, err = c.Git().List(ctx, owner+"/"+name+"@"+commit); err != nil {
		return nil, err
	}
	return model, nil
}

// Releases newest first, then branches and the tags without a release
func (githubAPI) Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error) {
	owner, name, err := ghSplit(repo)
	if err != nil {
		return nil, err
	}
	st, err := ghState(ctx, c, repo)
	if err != nil {
		return nil, err
	}
	latest := st.latest()
	var out []*v1.Revision
	seen := map[string]bool{}
	for i := range st.releases {
		r := &st.releases[i]
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
		rev := &v1.Revision{Name: r.TagName, Default: r == latest, Detail: detail, UpdatedAt: Stamp(r.PublishedAt)}
		for _, as := range r.Assets {
			rev.SizeBytes += uint64(as.Size)
		}
		out = append(out, rev)
		seen[r.TagName] = true
	}
	for _, kind := range []string{"branches", "tags"} {
		next := c.URL("repos", owner, name, kind) + "?per_page=" + strconv.Itoa(ghPageSize)
		err := eachPage(ctx, c, next, nil, func(page []ghRef) {
			for _, e := range page {
				if seen[e.Name] {
					continue
				}
				seen[e.Name] = true
				out = append(out, refRevision(e.Name, e.Commit.Sha, latest == nil && kind == "branches" && e.Name == st.defaultBranch, kind == "tags"))
			}
		})
		if err != nil {
			return nil, err
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
	text, err := c.HTTP().TextWith(ctx, c.URL("repos", owner, name, "readme"), q, http.Header{"Accept": {ghRawMedia}}, cardMax)
	if err != nil {
		return cardOrEmpty(page, err)
	}
	return &v1.ModelCard{Markdown: text, Url: page}, nil
}

// A release asset downloads from its link, a tree file comes through git
func (githubAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	st, err := ghState(ctx, c, model.GetRepo())
	if err != nil {
		return nil, err
	}
	if rel := st.release(model.GetRevision()); rel != nil {
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
