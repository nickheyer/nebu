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
	Reversible:    []string{SortLikes, SortUpdated, ghSortForks},
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

// Search sort keys by shared sort id, relevance is the API's own order
var ghSortKeys = map[string]string{
	SortLikes:   "stars",
	SortUpdated: "updated",
	ghSortForks: "forks",
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
	page := Offset(req.GetCursor())
	if page < 1 {
		page = 1
	}
	q := url.Values{"q": {strings.Join(terms, " ")}, "per_page": {strconv.Itoa(c.Limit(req))}, "page": {strconv.Itoa(page)}}
	if key := ghSortKeys[sort.ID]; key != "" {
		q.Set("sort", key)
		order := "desc"
		if sort.Ascending {
			order = "asc"
		}
		q.Set("order", order)
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
	if uint64(page*c.Limit(req)) < body.TotalCount && len(body.Items) > 0 {
		resp.NextCursor = strconv.Itoa(page + 1)
	}
	return resp, nil
}

func ghHit(it ghItem) *v1.SearchHit {
	hit := &v1.SearchHit{
		Repo:        it.FullName,
		Name:        it.Name,
		Author:      it.Owner.Login,
		Likes:       it.Stars,
		Description: Excerpt(it.Description, 240),
		Url:         it.HTMLURL,
		Private:     it.Private,
		Tags:        it.Topics,
		UpdatedAt:   Stamp(it.PushedAt),
		CreatedAt:   Stamp(it.CreatedAt),
		Extra:       map[string]string{},
	}
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
	owner, name, ok := strings.Cut(strings.Trim(strings.TrimSpace(repo), "/"), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("repo %q: want owner/name", repo)
	}
	return owner, strings.TrimSuffix(name, ".git"), nil
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
		for next != "" {
			var page []ghRelease
			header, err := c.JSON(ctx, next, nil, &page)
			if err != nil {
				return nil, err
			}
			st.releases = append(st.releases, page...)
			next = NextLink(header.Get("Link"), c.Base())
		}
		return st, nil
	})
}

// The newest release that is neither a draft nor a prerelease, else the newest of any kind
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
		detail := strings.TrimSuffix(strings.TrimSuffix(kind, "es"), "s")
		next := c.URL("repos", owner, name, kind) + "?per_page=" + strconv.Itoa(ghPageSize)
		for next != "" {
			var page []struct {
				Name   string `json:"name"`
				Commit struct {
					Sha string `json:"sha"`
				} `json:"commit"`
			}
			header, err := c.JSON(ctx, next, nil, &page)
			if err != nil {
				return nil, err
			}
			for _, e := range page {
				if seen[e.Name] {
					continue
				}
				seen[e.Name] = true
				out = append(out, &v1.Revision{Name: e.Name, Commit: e.Commit.Sha, Detail: detail, Default: latest == nil && kind == "branches" && e.Name == st.defaultBranch})
			}
			next = NextLink(header.Get("Link"), c.Base())
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
		if IsStatus(err, http.StatusNotFound) {
			return &v1.ModelCard{Url: page}, nil
		}
		return nil, err
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
