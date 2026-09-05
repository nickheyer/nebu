package sources

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Kaggle Models, one instance per framework and variant
var kaggle = &Catalog{
	ID:            "kaggle",
	Kind:          v1.SourceKind_SOURCE_KIND_KAGGLE,
	Name:          "Kaggle",
	Transports:    []Use{{Kind: TransportHTTP, Fields: map[string]string{"endpoint": "https://www.kaggle.com", "token_env": "KAGGLE_KEY", "username_env": "KAGGLE_USERNAME"}}},
	WebPath:       "/models",
	AuthRequired:  true,
	Description:   "Kaggle Models, one instance per framework and variant",
	RepoExample:   "owner/model/framework/instance",
	RepoPattern:   `^[\w.-]+/[\w.-]+(/[\w.-]+/[\w.-]+)?$`,
	RevisionLabel: "variant",
	Sorts:         []string{SortTrending, SortDownloads, SortLikes, SortUpdated, SortCreated, kgSortNotebooks},
	SortKeys: map[string]string{
		SortTrending:    "hotness",
		SortDownloads:   "downloadCount",
		SortLikes:       "voteCount",
		SortUpdated:     "updateTime",
		SortCreated:     "createTime",
		kgSortNotebooks: "notebookCount",
	},
	Facets:    []*v1.Facet{Freeform(FacetAuthor, "Owner")},
	HitFields: []*v1.ConfigField{{Name: "frameworks", Label: "Frameworks", Description: "Frameworks the model is published for"}},
	API:       kaggleAPI{},
}

func init() { register(kaggle) }

const (
	kgFilesPageSize = 200
	kgSortNotebooks = "notebooks"
)

// The Kaggle Models API: a token paged list, instances, versioned file lists, signed downloads
type kaggleAPI struct{}

type kgInstance struct {
	Slug          string `json:"slug"`
	Framework     string `json:"framework"`
	Overview      string `json:"overview"`
	Usage         string `json:"usage"`
	VersionNumber int    `json:"versionNumber"`
	LicenseName   string `json:"licenseName"`
}

type kgModel struct {
	Ref         string `json:"ref"`
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle"`
	Author      string `json:"author"`
	Slug        string `json:"slug"`
	IsPrivate   bool   `json:"isPrivate"`
	Description string `json:"description"`
	VoteCount   uint64 `json:"voteCount"`
	UpdateTime  string `json:"updateTime"`
	URL         string `json:"url"`
	Tags        []struct {
		Name string `json:"name"`
	} `json:"tags"`
	Instances []kgInstance `json:"instances"`
}

func (kaggleAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	q := url.Values{
		"sortBy":   {sort.Key},
		"pageSize": {strconv.Itoa(c.Limit(req))},
	}
	if query := strings.TrimSpace(req.GetQuery()); query != "" {
		q.Set("search", query)
	}
	if owner := Author(req); owner != "" {
		q.Set("owner", owner)
	}
	if cursor := strings.TrimSpace(req.GetCursor()); cursor != "" {
		q.Set("pageToken", cursor)
	}
	var body struct {
		Models        []kgModel `json:"models"`
		NextPageToken string    `json:"nextPageToken"`
		TotalResults  uint64    `json:"totalResults"`
	}
	if _, err := c.JSON(ctx, c.URL("api", "v1", "models", "list"), q, &body); err != nil {
		return nil, err
	}
	resp := &v1.SearchResponse{Hits: make([]*v1.SearchHit, 0, len(body.Models)), NextCursor: body.NextPageToken, Total: body.TotalResults}
	for _, it := range body.Models {
		if hit := kgHit(c, it); hit != nil {
			resp.Hits = append(resp.Hits, hit)
		}
	}
	return resp, nil
}

func kgHit(c *Client, it kgModel) *v1.SearchHit {
	if it.Ref == "" {
		return nil
	}
	owner, name := splitRepo(it.Ref)
	if it.Slug != "" {
		name = it.Slug
	}
	if it.Title != "" {
		name = it.Title
	}
	if it.Author != "" {
		owner = it.Author
	}
	hit := newHit(it.Ref, name, owner)
	hit.Likes, hit.Private, hit.Url, hit.UpdatedAt = it.VoteCount, it.IsPrivate, kgPageURL(c, it), Stamp(it.UpdateTime)
	summary := it.Subtitle
	if summary == "" {
		summary = kgFirstLine(it.Description)
	}
	hit.Description = Summary(summary)
	for _, t := range it.Tags {
		if t.Name != "" {
			hit.Tags = append(hit.Tags, t.Name)
		}
	}
	if len(it.Instances) > 0 {
		hit.Extra["instances"] = strconv.Itoa(len(it.Instances))
	}
	if fw := kgFrameworks(it.Instances); fw != "" {
		hit.Extra["frameworks"] = fw
	}
	for _, in := range it.Instances {
		if in.LicenseName != "" {
			hit.License = in.LicenseName
			break
		}
	}
	return hit
}

// Joins the distinct frameworks in first seen order
func kgFrameworks(instances []kgInstance) string {
	var out []string
	seen := map[string]bool{}
	for _, in := range instances {
		if in.Framework == "" || seen[in.Framework] {
			continue
		}
		seen[in.Framework] = true
		out = append(out, in.Framework)
	}
	return strings.Join(out, ", ")
}

// Returns the first line of a markdown body without its heading marks
func kgFirstLine(md string) string {
	for _, line := range strings.Split(md, "\n") {
		if line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#")); line != "" {
			return line
		}
	}
	return ""
}

// Human page for a model, the API's own URL when it sends one
func kgPageURL(c *Client, it kgModel) string {
	if it.URL != "" {
		return it.URL
	}
	return c.Page("models", it.Ref)
}

// Names one instance of a model, version 0 meaning latest
type kgLocator struct {
	owner, model, framework, instance string
	version                           int
}

func (l kgLocator) modelRepo() string { return l.owner + "/" + l.model }

func (l kgLocator) repo() string { return l.modelRepo() + "/" + l.framework + "/" + l.instance }

func (l kgLocator) hasInstance() bool { return l.framework != "" && l.instance != "" }

// Reads a version number, with or without the v prefix Commit uses
func kgParseVersion(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(s), "v"))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("version %q: want a positive number", s)
	}
	return n, nil
}

// Reads owner/model with a framework/instance[/version] revision, or the variant repo with a version
func kgParse(repo, revision string) (kgLocator, error) {
	var l kgLocator
	parts, err := segments("repo", repo, "owner/model or owner/model/framework/instance", 2, 4)
	if err != nil {
		return l, err
	}
	l.owner, l.model = parts[0], parts[1]
	if len(parts) == 4 {
		l.framework, l.instance = parts[2], parts[3]
	}
	if strings.TrimSpace(revision) == "" {
		return l, nil
	}
	if len(parts) == 4 {
		v, err := kgParseVersion(revision)
		if err != nil {
			return l, err
		}
		l.version = v
		return l, nil
	}
	rp, err := segments("revision", revision, "framework/instance[/version]", 2, 3)
	if err != nil {
		return l, err
	}
	l.framework, l.instance = rp[0], rp[1]
	if len(rp) == 3 {
		v, err := kgParseVersion(rp[2])
		if err != nil {
			return l, err
		}
		l.version = v
	}
	return l, nil
}

// Fetches a model with its instances
func kgGetModel(ctx context.Context, c *Client, l kgLocator) (kgModel, error) {
	var it kgModel
	_, err := c.JSON(ctx, c.URL("api", "v1", "models", l.modelRepo(), "get"), nil, &it)
	if it.Ref == "" {
		it.Ref = l.modelRepo()
	}
	return it, err
}

// Fetches one instance with its latest version number
func kgGetInstance(ctx context.Context, c *Client, l kgLocator) (kgInstance, error) {
	var in kgInstance
	_, err := c.JSON(ctx, c.URL("api", "v1", "models", l.repo(), "get"), nil, &in)
	return in, err
}

// Fills in the first instance and the latest version when the locator leaves them open
func kgPin(ctx context.Context, c *Client, l kgLocator) (kgLocator, error) {
	if !l.hasInstance() {
		it, err := kgGetModel(ctx, c, l)
		if err != nil {
			return l, err
		}
		if len(it.Instances) == 0 {
			return l, fmt.Errorf("%s: no instances", l.modelRepo())
		}
		first := it.Instances[0]
		l.framework, l.instance, l.version = first.Framework, first.Slug, first.VersionNumber
	}
	if l.version == 0 {
		in, err := kgGetInstance(ctx, c, l)
		if err != nil {
			return l, err
		}
		l.version = in.VersionNumber
	}
	if l.version == 0 {
		return l, fmt.Errorf("%s: no versions", l.repo())
	}
	return l, nil
}

func (kaggleAPI) Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error) {
	l, err := kgParse(repo, revision)
	if err != nil {
		return nil, err
	}
	if l, err = kgPin(ctx, c, l); err != nil {
		return nil, err
	}
	version := strconv.Itoa(l.version)
	model := &v1.Model{Repo: l.repo(), Revision: version, Commit: "v" + version}
	filesURL := c.URL("api", "v1", "models", l.repo(), version, "files")
	token := ""
	for {
		q := url.Values{"pageSize": {strconv.Itoa(kgFilesPageSize)}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var page struct {
			Files []struct {
				Name string `json:"name"`
				Size uint64 `json:"size"`
			} `json:"files"`
			NextPageToken string `json:"nextPageToken"`
		}
		if _, err := c.JSON(ctx, filesURL, q, &page); err != nil {
			return nil, err
		}
		for _, f := range page.Files {
			if f.Name == "" {
				continue
			}
			model.Artifacts = append(model.Artifacts, &v1.Artifact{Path: f.Name, SizeBytes: f.Size})
		}
		if page.NextPageToken == "" || page.NextPageToken == token {
			break
		}
		token = page.NextPageToken
	}
	return model, nil
}

func (kaggleAPI) Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error) {
	l, err := kgParse(repo, "")
	if err != nil {
		return nil, err
	}
	if !l.hasInstance() {
		it, err := kgGetModel(ctx, c, l)
		if err != nil {
			return nil, err
		}
		out := make([]*v1.Revision, 0, len(it.Instances))
		for i, in := range it.Instances {
			if in.Framework == "" || in.Slug == "" {
				continue
			}
			v := l
			v.framework, v.instance = in.Framework, in.Slug
			r := &v1.Revision{Name: in.Framework + "/" + in.Slug, Repo: v.repo(), Detail: in.Framework, Default: i == 0}
			if in.VersionNumber > 0 {
				r.Commit = "v" + strconv.Itoa(in.VersionNumber)
			}
			out = append(out, r)
		}
		return out, nil
	}
	in, err := kgGetInstance(ctx, c, l)
	if err != nil {
		return nil, err
	}
	out := make([]*v1.Revision, 0, in.VersionNumber)
	for n := in.VersionNumber; n >= 1; n-- {
		v := strconv.Itoa(n)
		out = append(out, &v1.Revision{Name: v, Commit: "v" + v, Detail: in.Framework, Default: n == in.VersionNumber})
	}
	return out, nil
}

func (kaggleAPI) Card(ctx context.Context, c *Client, repo, revision string) (*v1.ModelCard, error) {
	l, err := kgParse(repo, revision)
	if err != nil {
		return nil, err
	}
	it, err := kgGetModel(ctx, c, l)
	if err != nil {
		return cardOrEmpty(kgPageURL(c, it), err)
	}
	card := &v1.ModelCard{Markdown: it.Description, Url: kgPageURL(c, it)}
	if !l.hasInstance() {
		return card, nil
	}
	in, err := kgGetInstance(ctx, c, l)
	if err != nil {
		if IsStatus(err, http.StatusNotFound) {
			return card, nil
		}
		return nil, err
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(card.Markdown))
	section := func(title, body string) {
		if body = strings.TrimSpace(body); body == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "## %s\n\n%s", title, body)
	}
	section("Overview", in.Overview)
	section("Usage", in.Usage)
	card.Markdown = b.String()
	return card, nil
}

func (kaggleAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	return c.Range(ctx, c.URL("api", "v1", "models", model.GetRepo(), model.GetRevision(), "download", artifact.GetPath()), artifact)
}
