package sources

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The NVIDIA NGC catalog
var ngc = &Catalog{
	ID:   "ngc",
	Kind: v1.SourceKind_SOURCE_KIND_NGC,
	Name: "NVIDIA NGC",
	Transports: []Use{
		httpUse("https://api.ngc.nvidia.com", "NGC_API_KEY"),
		{Kind: TransportHTTP, Name: "auth", Fields: map[string]string{"endpoint": "https://authn.nvidia.com"}},
	},
	Web:           "https://catalog.ngc.nvidia.com",
	WebPath:       "/models",
	Description:   "NVIDIA NGC catalog, checkpoints for NVIDIA's own frameworks",
	RepoExample:   "org/team/model",
	RepoPattern:   `^[\w.-]+(/[\w.-]+){1,2}$`,
	RevisionLabel: "version",
	Sorts:         []string{SortDownloads, SortRelevance, SortUpdated, SortCreated, SortName},
	SortKeys: map[string]string{
		SortRelevance: "score",
		SortDownloads: "weightPopular",
		SortUpdated:   "dateModified",
		SortCreated:   "dateCreated",
		SortName:      "name",
	},
	Reversible: []string{SortDownloads, SortRelevance, SortUpdated, SortCreated, SortName},
	Facets: []*v1.Facet{
		Freeform(FacetFramework, "Framework"),
		Freeform(FacetPublisher, "Publisher"),
	},
	API: ngcAPI{},
}

func init() { register(ngc) }

const (
	ngcResourceModel   = "MODEL"
	ngcFilePageSize    = 1000
	ngcVersionPageSize = 100
	ngcTokenTTL        = 50 * time.Minute
	ngcRetryAfter      = time.Minute
)

// Sorts whose natural order runs ascending, the rest run descending
var ngcAscending = map[string]bool{SortName: true}

// Search filter fields by shared facet id
var ngcFilterFields = []struct{ facet, field string }{
	{FacetFramework, "framework"},
	{FacetPublisher, "publisher"},
}

// Label keys that describe a model
var ngcTagLabels = map[string]bool{"general": true, "framework": true, "precision": true, "publisher": true, "builtBy": true}

// The NGC API: query search, versioned file lists, an API key swapped for a bearer
type ngcAPI struct{}

// Exchanges the API key at the token service, keeping the bearer for a while
type ngcAuth struct {
	auth  *HTTP
	key   string
	mu    sync.Mutex
	token string
	until time.Time
}

// Installs the exchange on the API transport, so requests carry the bearer, never the key
func (ngcAPI) Check(c *Client) error {
	auth, ok := c.Transport("auth").(*HTTP)
	if !ok {
		return fmt.Errorf("auth endpoint is required")
	}
	a := &ngcAuth{auth: auth, key: c.Token()}
	c.HTTP().SetAuthorizer(a.headers)
	return nil
}

// Returns the bearer header for catalog requests, none as a guest
func (a *ngcAuth) headers(ctx context.Context) http.Header {
	if a.key == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Now().After(a.until) {
		a.token = a.exchange(ctx)
		a.until = time.Now().Add(ngcTokenTTL)
		if a.token == "" {
			a.until = time.Now().Add(ngcRetryAfter)
		}
	}
	if a.token == "" {
		return nil
	}
	return http.Header{"Authorization": {"Bearer " + a.token}}
}

// Asks the auth service for a bearer, empty on any failure
func (a *ngcAuth) exchange(ctx context.Context) string {
	q := url.Values{"service": {"ngc"}}
	header := http.Header{"Authorization": {"ApiKey " + a.key}, "Accept": {"application/json"}}
	resp, err := a.auth.Do(ctx, http.MethodGet, a.auth.URL("token"), q, header)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ""
	}
	return body.Token
}

type ngcResource struct {
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
	OrgName      string `json:"orgName"`
	TeamName     string `json:"teamName"`
	Name         string `json:"name"`
	DisplayName  string `json:"displayName"`
	Description  string `json:"description"`
	DateCreated  string `json:"dateCreated"`
	DateModified string `json:"dateModified"`
	IsPublic     bool   `json:"isPublic"`
	GuestAccess  bool   `json:"guestAccess"`
	Labels       []struct {
		Key    string   `json:"key"`
		Values []string `json:"values"`
	} `json:"labels"`
	Attributes []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"attributes"`
}

func (ngcAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	query := strings.TrimSpace(req.GetQuery())
	id := sort.ID
	// Score means nothing without a query, popularity does
	if id == SortRelevance && query == "" {
		id = SortDownloads
	}
	page := Offset(req.GetCursor())
	type order struct {
		Field string `json:"field"`
		Value string `json:"value"`
	}
	filters := []order{}
	for _, f := range ngcFilterFields {
		for _, v := range Filter(req, f.facet) {
			filters = append(filters, order{Field: f.field, Value: v})
		}
	}
	data, err := json.Marshal(map[string]any{
		"query":    query,
		"page":     page,
		"pageSize": c.Limit(req),
		"orderBy":  []order{{Field: c.cat.SortKeys[id], Value: direction(sort.Ascending != ngcAscending[id], "ASC", "DESC")}},
		"filters":  filters,
	})
	if err != nil {
		return nil, err
	}
	var body struct {
		ResultPageTotal int    `json:"resultPageTotal"`
		ResultTotal     uint64 `json:"resultTotal"`
		Results         []struct {
			Resources []ngcResource `json:"resources"`
		} `json:"results"`
	}
	if _, err := c.JSON(ctx, c.URL("v2", "search", "catalog", "resources", ngcResourceModel), url.Values{"q": {string(data)}}, &body); err != nil {
		return nil, err
	}
	resp := &v1.SearchResponse{Total: body.ResultTotal}
	// Groups repeat resources, and the rest of the noise is test entries
	seen := map[string]bool{}
	for _, g := range body.Results {
		for _, r := range g.Resources {
			if r.ResourceID == "" || seen[r.ResourceID] || !r.IsPublic || (r.ResourceType != "" && r.ResourceType != ngcResourceModel) || ngcNumberedOrg(r.ResourceID) {
				continue
			}
			seen[r.ResourceID] = true
			resp.Hits = append(resp.Hits, ngcHit(c, r))
		}
	}
	if page+1 < body.ResultPageTotal {
		resp.NextCursor = strconv.Itoa(page + 1)
	}
	return resp, nil
}

// A label value that says nothing
func ngcPlaceholder(v string) bool {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(strings.ToUpper(v), "NSPECT-") {
		return true
	}
	switch strings.ToLower(v) {
	case "", "-", "n/a", "other", "true", "false":
		return true
	}
	return false
}

// Reports an org named by a number, which is a test org
func ngcNumberedOrg(resourceID string) bool {
	org, _, _ := strings.Cut(resourceID, "/")
	return org != "" && strings.Trim(org, "0123456789") == ""
}

func ngcHit(c *Client, r ngcResource) *v1.SearchHit {
	labels := map[string][]string{}
	for _, l := range r.Labels {
		labels[l.Key] = append(labels[l.Key], l.Values...)
	}
	attrs := map[string]string{}
	for _, a := range r.Attributes {
		attrs[a.Key] = a.Value
	}
	name := r.DisplayName
	if name == "" {
		name = r.Name
	}
	author := r.OrgName
	if p := labels["publisher"]; len(p) > 0 && p[0] != "" {
		author = p[0]
	}
	hit := newHit(r.ResourceID, name, author)
	hit.Description, hit.Task, hit.Gated = Summary(r.Description), attrs["application"], !r.GuestAccess
	hit.Url = ngcPageURL(c, r.OrgName, r.TeamName, r.Name)
	hit.CreatedAt, hit.UpdatedAt = Stamp(r.DateCreated), Stamp(r.DateModified)
	if strings.EqualFold(hit.Task, "other") {
		hit.Task = ""
	}
	// Labels repeat across keys, and a tag list must not
	seen := map[string]bool{}
	for _, l := range r.Labels {
		if !ngcTagLabels[l.Key] {
			continue
		}
		for _, v := range l.Values {
			if ngcPlaceholder(v) || seen[v] {
				continue
			}
			seen[v] = true
			hit.Tags = append(hit.Tags, v)
		}
	}
	if n, err := strconv.ParseUint(attrs["latestVersionSizeInBytes"], 10, 64); err == nil {
		hit.SizeBytes = n
	}
	for k, v := range map[string]string{
		"format":    attrs["format"],
		"version":   attrs["latestVersionIdStr"],
		"framework": strings.Join(labels["framework"], ","),
		"precision": strings.Join(labels["precision"], ","),
	} {
		if v != "" {
			hit.Extra[k] = v
		}
	}
	return hit
}

type ngcModelInfo struct {
	Description        string `json:"description"`
	ShortDescription   string `json:"shortDescription"`
	LatestVersionIDStr string `json:"latestVersionIdStr"`
}

type ngcPagination struct {
	NextPage json.RawMessage `json:"nextPage"`
}

// Reads the next page marker, which arrives as a string or a number
func (p ngcPagination) next() string {
	s := strings.Trim(strings.TrimSpace(string(p.NextPage)), `"`)
	if s == "null" {
		return ""
	}
	return s
}

// Reads the model record that names the latest version
func ngcInfo(ctx context.Context, c *Client, org, team, name string) (*ngcModelInfo, error) {
	var body struct {
		Model ngcModelInfo `json:"model"`
	}
	if _, err := c.JSON(ctx, c.URL("v2", "models", org, team, name), nil, &body); err != nil {
		return nil, err
	}
	return &body.Model, nil
}

func (ngcAPI) Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error) {
	org, team, name, err := ngcSplit(repo)
	if err != nil {
		return nil, err
	}
	if revision == "" {
		info, err := ngcInfo(ctx, c, org, team, name)
		if err != nil {
			return nil, err
		}
		revision = info.LatestVersionIDStr
		if revision == "" {
			return nil, fmt.Errorf("%s: no versions published", repo)
		}
	}
	model := &v1.Model{Repo: ngcJoin(org, team, name), Revision: revision, Commit: "v" + revision}
	rawURL := c.URL("v2", "models", org, team, name, "versions", revision, "files")
	q := url.Values{"page-size": {strconv.Itoa(ngcFilePageSize)}}
	for {
		var body struct {
			ModelFiles []struct {
				Path         string `json:"path"`
				SizeInBytes  uint64 `json:"sizeInBytes"`
				Sha256Base64 string `json:"sha256_base64"`
			} `json:"modelFiles"`
			PaginationInfo ngcPagination `json:"paginationInfo"`
		}
		if _, err := c.JSON(ctx, rawURL, q, &body); err != nil {
			return nil, err
		}
		for _, f := range body.ModelFiles {
			if f.Path == "" {
				continue
			}
			model.Artifacts = append(model.Artifacts, &v1.Artifact{Path: f.Path, SizeBytes: f.SizeInBytes, Sha256: ngcHexDigest(f.Sha256Base64)})
		}
		next := body.PaginationInfo.next()
		if next == "" || next == q.Get("page-number") {
			break
		}
		q.Set("page-number", next)
	}
	return model, nil
}

// Turns the catalog's base64 digest into lower case hex, empty unless it is a sha256
func ngcHexDigest(b64 string) string {
	b64 = strings.TrimSpace(b64)
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(b64); err != nil {
			return ""
		}
	}
	if len(raw) != sha256.Size {
		return ""
	}
	return hex.EncodeToString(raw)
}

func (ngcAPI) Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error) {
	org, team, name, err := ngcSplit(repo)
	if err != nil {
		return nil, err
	}
	info, err := ngcInfo(ctx, c, org, team, name)
	if err != nil {
		return nil, err
	}
	rawURL := c.URL("v2", "models", org, team, name, "versions")
	q := url.Values{"page-size": {strconv.Itoa(ngcVersionPageSize)}}
	var out []*v1.Revision
	for {
		var body struct {
			ModelVersions []struct {
				VersionID        string `json:"versionId"`
				Description      string `json:"description"`
				CreatedDate      string `json:"createdDate"`
				TotalSizeInBytes uint64 `json:"totalSizeInBytes"`
			} `json:"modelVersions"`
			PaginationInfo ngcPagination `json:"paginationInfo"`
		}
		if _, err := c.JSON(ctx, rawURL, q, &body); err != nil {
			return nil, err
		}
		for _, v := range body.ModelVersions {
			if v.VersionID == "" {
				continue
			}
			out = append(out, &v1.Revision{
				Name:      v.VersionID,
				Default:   v.VersionID == info.LatestVersionIDStr,
				SizeBytes: v.TotalSizeInBytes,
				UpdatedAt: Stamp(v.CreatedDate),
				Detail:    Summary(v.Description),
			})
		}
		next := body.PaginationInfo.next()
		if next == "" || next == q.Get("page-number") {
			break
		}
		q.Set("page-number", next)
	}
	// Newest first
	slices.SortStableFunc(out, func(a, b *v1.Revision) int {
		return stamp(b.GetUpdatedAt()).Compare(stamp(a.GetUpdatedAt()))
	})
	return out, nil
}

func (ngcAPI) Card(ctx context.Context, c *Client, repo, revision string) (*v1.ModelCard, error) {
	org, team, name, err := ngcSplit(repo)
	if err != nil {
		return nil, err
	}
	card := &v1.ModelCard{Url: ngcPageURL(c, org, team, name)}
	info, err := ngcInfo(ctx, c, org, team, name)
	if err != nil {
		return cardOrEmpty(card.Url, err)
	}
	card.Markdown = info.Description
	if card.Markdown == "" {
		card.Markdown = info.ShortDescription
	}
	return card, nil
}

func (ngcAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	org, team, name, err := ngcSplit(model.GetRepo())
	if err != nil {
		return nil, err
	}
	if model.GetRevision() == "" {
		return nil, fmt.Errorf("%s: no version", model.GetRepo())
	}
	// The file URL answers with a redirect to a signed URL, ranges follow it
	return c.Range(ctx, c.URL("v2", "models", org, team, name, "versions", model.GetRevision(), "files", artifact.GetPath()), artifact)
}

// Splits org/team/name or org/name
func ngcSplit(repo string) (org, team, name string, err error) {
	parts, err := segments("repo", repo, "org/name or org/team/name", 2, 3)
	if err != nil {
		return "", "", "", err
	}
	if len(parts) == 2 {
		return parts[0], "", parts[1], nil
	}
	return parts[0], parts[1], parts[2], nil
}

// Joins the parts back into the repo nebu stores
func ngcJoin(org, team, name string) string {
	if team == "" {
		return org + "/" + name
	}
	return org + "/" + team + "/" + name
}

// Builds the human catalog page for a model
func ngcPageURL(c *Client, org, team, name string) string {
	if team == "" {
		return c.Page("orgs", org, "models", name)
	}
	return c.Page("orgs", org, "teams", team, "models", name)
}
