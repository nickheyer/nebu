package sources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ModelScope, the hub run by Alibaba
var modelscope = &Catalog{
	ID:            "modelscope",
	Kind:          v1.SourceKind_SOURCE_KIND_MODELSCOPE,
	Name:          "ModelScope",
	Transports:    []Use{httpUse("https://www.modelscope.cn", "MODELSCOPE_API_TOKEN")},
	Description:   "ModelScope, the hub run by Alibaba",
	RepoExample:   "org/model",
	RepoPattern:   `^[\w.-]+/[\w.-]+$`,
	RevisionLabel: "revision",
	Sorts:         []string{SortRelevance, SortDownloads, SortLikes, SortUpdated},
	SortKeys: map[string]string{
		SortRelevance: "Default",
		SortDownloads: "DownloadsCount",
		SortLikes:     "StarsCount",
		SortUpdated:   "GmtModified",
	},
	Noise:     []string{".+:.+", "[a-z]{2,3}"},
	HitFields: []*v1.ConfigField{{Name: "architecture", Label: "Architecture", Description: "Model architecture"}},
	API:       modelscopeAPI{},
}

func init() { register(modelscope) }

const (
	msRevision      = "master"
	msPublicVisible = 5
)

// Criterion categories by facet id
var msCriteria = map[string]string{
	FacetTask:    "tasks",
	FacetLibrary: "libraries",
	FacetLicense: "license",
	FacetTag:     "tags",
}

// The ModelScope API: a PUT search with criteria, a recursive file list, revisions, raw files
type modelscopeAPI struct{}

// The status every ModelScope answer carries
type msEnvelope struct {
	Code    int    `json:"Code"`
	Message string `json:"Message"`
}

// The error an answer reports under what, none when its code says it went through
func (e msEnvelope) err(what string) error {
	if e.Code != 0 && e.Code != 200 {
		return fmt.Errorf("%s: %s", what, e.Message)
	}
	return nil
}

// Reads the task taxonomy the hub publishes
func (modelscopeAPI) Facets(ctx context.Context, c *Client) ([]*v1.Facet, error) {
	var tree struct {
		Data struct {
			Domains []struct {
				DomainName string `json:"DomainName"`
				Tasks      []struct {
					Name string `json:"Name"`
				} `json:"Tasks"`
			} `json:"Domains"`
		} `json:"Data"`
	}
	if _, err := c.JSON(ctx, c.URL("api", "v1", "tasks"), nil, &tree); err != nil {
		return nil, err
	}
	task := NewFacet(FacetTask, "Task", false)
	for _, d := range tree.Data.Domains {
		for _, t := range d.Tasks {
			if t.Name != "" {
				task.Values = append(task.Values, GroupedValue(t.Name, "", d.DomainName))
			}
		}
	}
	return []*v1.Facet{
		task,
		Freeform(FacetLibrary, "Library"),
		Freeform(FacetLicense, "License"),
		Freeform(FacetTag, "Tag"),
	}, nil
}

type msSearchBody struct {
	msEnvelope
	Data struct {
		Model struct {
			TotalCount uint64           `json:"TotalCount"`
			Models     []map[string]any `json:"Models"`
		} `json:"Model"`
	} `json:"Data"`
}

func (modelscopeAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	limit, page := c.Limit(req), c.page(req)
	var criterion []map[string]any
	for facet, category := range msCriteria {
		if values := Filter(req, facet); len(values) > 0 {
			criterion = append(criterion, map[string]any{"category": category, "predicate": "contains", "values": values})
		}
	}
	if len(req.GetTags()) > 0 {
		criterion = append(criterion, map[string]any{"category": "tags", "predicate": "contains", "values": req.GetTags()})
	}
	body := map[string]any{"Name": strings.TrimSpace(req.GetQuery()), "PageNumber": page, "PageSize": limit, "SortBy": sort.Key, "Criterion": criterion}
	var root msSearchBody
	if _, err := c.JSONBody(ctx, http.MethodPut, c.URL("api", "v1", "dolphin", "models"), nil, body, &root); err != nil {
		return nil, err
	}
	if err := root.err("modelscope"); err != nil {
		return nil, err
	}
	resp := &v1.SearchResponse{Total: root.Data.Model.TotalCount}
	for _, m := range root.Data.Model.Models {
		if hit := msHit(c, m); hit != nil {
			resp.Hits = append(resp.Hits, hit)
		}
	}
	c.nextPage(resp, page, limit, resp.Total)
	return resp, nil
}

func msHit(c *Client, m map[string]any) *v1.SearchHit {
	owner, _ := m["Path"].(string)
	name, _ := m["Name"].(string)
	if owner == "" || name == "" {
		return nil
	}
	hit := newHit(owner+"/"+name, name, owner)
	hit.Url = msPage(c, hit.Repo)
	if n, err := eval.Number(m["Downloads"]); err == nil {
		hit.Downloads = uint64(n)
	}
	if n, err := eval.Number(m["Stars"]); err == nil {
		hit.Likes = uint64(n)
	}
	if n, err := eval.Number(m["LastUpdatedTime"]); err == nil && n > 0 {
		hit.UpdatedAt = timestamppb.New(time.Unix(int64(n), 0))
	}
	if n, err := eval.Number(m["CreatedTime"]); err == nil && n > 0 {
		hit.CreatedAt = timestamppb.New(time.Unix(int64(n), 0))
	}
	if n, err := eval.Number(m["Visibility"]); err == nil && n != 0 && n != msPublicVisible {
		hit.Private = true
	}
	if d, _ := m["Description"].(string); d != "" {
		hit.Description = Summary(d)
	}
	hit.License, _ = m["License"].(string)
	hit.Tags = append(hit.Tags, msStrings(m["Tags"])...)
	if libs := msStrings(m["Libraries"]); len(libs) > 0 {
		hit.Library = libs[0]
		hit.Tags = append(hit.Tags, libs...)
	}
	if tasks, ok := m["Tasks"].([]any); ok {
		for _, t := range tasks {
			if tm, ok := t.(map[string]any); ok {
				if n, _ := tm["Name"].(string); n != "" {
					if hit.Task == "" {
						hit.Task = n
					}
					hit.Tags = append(hit.Tags, n)
				}
			}
		}
	}
	if arch := msStrings(m["Architectures"]); len(arch) > 0 {
		hit.Extra["architecture"] = arch[0]
	}
	if cn, _ := m["ChineseName"].(string); cn != "" && cn != name {
		hit.Extra["title"] = cn
	}
	return hit
}

// The model page on the site
func msPage(c *Client, repo string) string { return c.Page("models", repo) }

func msStrings(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range list {
		if s := eval.Scalar(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

type msFile struct {
	Path     string `json:"Path"`
	Size     uint64 `json:"Size"`
	Sha256   string `json:"Sha256"`
	Type     string `json:"Type"`
	Revision string `json:"Revision"`
}

func (modelscopeAPI) Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error) {
	if revision == "" {
		revision = msRevision
	}
	var resp struct {
		msEnvelope
		Data struct {
			Files []msFile `json:"Files"`
		} `json:"Data"`
	}
	q := url.Values{"Revision": {revision}, "Recursive": {"true"}}
	if _, err := c.JSON(ctx, c.URL("api", "v1", "models", repo, "repo", "files"), q, &resp); err != nil {
		return nil, err
	}
	if err := resp.err(repo); err != nil {
		return nil, err
	}
	model := &v1.Model{Repo: repo, Revision: revision}
	revisions := map[string]bool{}
	h := sha256.New()
	for _, f := range resp.Data.Files {
		if f.Type != "blob" {
			continue
		}
		model.Artifacts = append(model.Artifacts, &v1.Artifact{Path: f.Path, SizeBytes: f.Size, Sha256: Hex(f.Sha256)})
		revisions[f.Revision] = true
		fmt.Fprintf(h, "%s\x00%s\x00", f.Path, f.Revision)
	}
	// One shared file revision is the repo commit
	switch len(revisions) {
	case 0:
	case 1:
		for r := range revisions {
			model.Commit = r
		}
	default:
		model.Commit = "tree-" + hex.EncodeToString(h.Sum(nil))[:12]
	}
	return model, nil
}

type msRef struct {
	Revision  string `json:"Revision"`
	CreatedAt int64  `json:"CreatedAt"`
}

func (modelscopeAPI) Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error) {
	var body struct {
		msEnvelope
		Data struct {
			RevisionMap struct {
				Branches []msRef `json:"Branches"`
				Tags     []msRef `json:"Tags"`
			} `json:"RevisionMap"`
		} `json:"Data"`
	}
	if _, err := c.JSON(ctx, c.URL("api", "v1", "models", repo, "revisions"), nil, &body); err != nil {
		return nil, err
	}
	if err := body.err(repo); err != nil {
		return nil, err
	}
	var out []*v1.Revision
	add := func(e msRef, tag bool) {
		r := refRevision(e.Revision, "", e.Revision == msRevision, tag)
		if e.CreatedAt > 0 {
			r.UpdatedAt = timestamppb.New(time.Unix(e.CreatedAt, 0))
		}
		out = append(out, r)
	}
	for _, b := range body.Data.RevisionMap.Branches {
		add(b, false)
	}
	for _, t := range body.Data.RevisionMap.Tags {
		add(t, true)
	}
	return out, nil
}

func (modelscopeAPI) Card(ctx context.Context, c *Client, repo, revision string) (*v1.ModelCard, error) {
	if revision == "" {
		revision = msRevision
	}
	q := url.Values{"Revision": {revision}, "FilePath": {"README.md"}}
	card, err := c.CardText(ctx, c.URL("api", "v1", "models", repo, "repo"), q, msPage(c, repo))
	if err != nil {
		return nil, err
	}
	// A missing file answers with a JSON envelope rather than a 404
	if strings.HasPrefix(strings.TrimSpace(card.Markdown), "{") {
		card.Markdown = ""
	}
	return card, nil
}

func (modelscopeAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	q := url.Values{"Revision": {model.GetRevision()}, "FilePath": {artifact.GetPath()}}
	return c.Range(ctx, c.URL("api", "v1", "models", model.GetRepo(), "repo")+"?"+q.Encode(), artifact)
}
