package sources

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
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
	// Default is the hub's own ranking, which its site calls trending. It also accepts VisitsCount
	// and Alphabetic, then fails every search ordered by them.
	Sorts: []string{SortTrending, SortDownloads, SortLikes, SortUpdated, SortCreated},
	SortKeys: map[string]string{
		SortTrending:  "Default",
		SortDownloads: "DownloadsCount",
		SortLikes:     "StarsCount",
		SortUpdated:   "GmtModified",
		SortCreated:   "GmtCreated",
	},
	// The hub keeps its own ranking one way round
	Reversible: []string{SortDownloads, SortLikes, SortUpdated, SortCreated},
	// Filters the hub publishes no list for, after the facets read from it. The relations are those
	// its model list offered on 2026-11-19.
	Facets: []*v1.Facet{
		Hint(NewFacet(msFacetService, "Service", false, msServiceValues()...), "Services the hub runs the model on"),
		Hint(NewFacet(msFacetRelation, "Relation to base", false, Value("finetune", ""), Value("adapter", ""), Value("quantized", ""), Value("merge", "")), "How the model derives from its base model"),
		Hint(Multi(Freeform(FacetBaseModel, "Base model")), "Finetunes, adapters, quantizations, and merges of a repository"),
		Hint(Freeform(FacetAuthor, "Owner"), "A user or organization namespace"),
	},
	Noise: []string{".+:.+", "[a-z]{2,3}"},
	Columns: slices.Concat(
		Shown(Cols(ColSize, ColParameters, ColDownloads, ColLikes)...),
		Cols(ColUpdated, ColCreated, ColTask, ColLibrary, ColLicense, ColAuthor, ColDescription),
		[]*v1.HitColumn{
			ExtraCol("title", "Title", AsText, "The name the hub shows, often in Chinese"),
			ExtraCol("architecture", "Architecture", AsText, "Model architecture its config or GGUF header names"),
			ExtraCol("model_type", "Model type", AsList, "The model type its config names, such as qwen3"),
			ExtraCol("base_model", "Base model", AsList, "Repositories the model derives from"),
			ExtraCol("relation", "Relation to base", AsText, "Finetune, adapter, quantized, or merge"),
			ExtraCol("languages", "Languages", AsList, "Languages the model card names"),
			ExtraCol("dtypes", "Tensor types", AsList, "Tensor data types of the safetensors weights"),
			ExtraCol("aigc_type", "AIGC type", AsText, "Checkpoint or LoRA, for models of the hub's AIGC zone"),
		},
	),
	API: modelscopeAPI{},
}

func init() { register(modelscope) }

const msPublicVisible = 5

// Facets only ModelScope offers
const (
	msFacetLanguage  = "language"
	msFacetModelType = "model_type"
	msFacetScience   = "science"
	msFacetService   = "service"
	msFacetRelation  = "relation"
)

// A facet filled from the counts the search answers with, the most used values first
type msAgg struct {
	// The count list in the answer, the criterion category the facet's values go under
	agg, category   string
	id, label, hint string
	// The label of a value, the value itself when nil
	name func(string) string
}

// Value lists the search counts, by the facet each fills. The hub cuts each list at 100.
var msAggs = []msAgg{
	{agg: "libraries", category: "libraries", id: FacetLibrary, label: "Library"},
	{agg: "license", category: "license", id: FacetLicense, label: "License", hint: "The most used licenses, as their models spell them"},
	{agg: "language", category: "languages", id: msFacetLanguage, label: "Language", hint: "The most used languages, by code"},
	{agg: "model_type", category: "model_type", id: msFacetModelType, label: "Model type", hint: "The most used model types configs name, such as qwen3"},
	{agg: "tags", category: "tags", id: FacetTag, label: "Tag", hint: "The most used tags"},
	{agg: "nexa_catalog", category: "nexa_catalogs", id: msFacetScience, label: "Science field", hint: "Fields of the hub's science zone", name: Humanize},
}

// Organizations offered to pick from, the hub listing them by its own standing. Any other
// namespace is reached through the owner facet.
const msOrganizations = 100

// A service the hub runs models on, which its search matches as a flag set to 1, or as a capability
// of its PAI Model Gallery listing. Ids are the site's own.
type msService struct{ id, label, group, flag, pai string }

// The services the hub's model list offered on 2026-11-19, grouped as it groups them
var msServices = []msService{
	{id: "inference_type", label: "API-Inference", group: "Inference", flag: "inference_type"},
	{id: "demo", label: "Model demo", group: "Inference", flag: "support_experience"},
	{id: "inference", label: "RESTful inference API", group: "Inference", flag: "support_api_inference"},
	{id: "flexTrain", label: "FlexTrain", group: "Training", flag: "support_flex_train"},
	{id: "train", label: "Fine-tuning with the ModelScope SDK", group: "Training", flag: "support_finetuning"},
	{id: "paiModelTrain", label: "PAI Model Gallery training", group: "Training", pai: "train"},
	{id: "swingDeploy", label: "SwingDeploy", group: "Deployment", flag: "support_deployment"},
	{id: "paiModelDeployment", label: "PAI Model Gallery deployment", group: "Deployment", pai: "deploy"},
	{id: "pivotEval", label: "PivotEval", group: "Evaluation", flag: "support_pivot_eval"},
	{id: "paiModelEvaluation", label: "PAI Model Gallery evaluation", group: "Evaluation", pai: "evaluation"},
}

// The services as the values of their facet
func msServiceValues() []*v1.FacetValue {
	out := make([]*v1.FacetValue, 0, len(msServices))
	for _, s := range msServices {
		out = append(out, GroupedValue(s.id, s.label, s.group))
	}
	return out
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

// A task of the hub's taxonomy, a subtask under it when the task groups several
type msTask struct {
	Name  string   `json:"Name"`
	Tasks []msTask `json:"Tasks"`
}

// Reads the task taxonomy, the value lists the search counts, and the organizations the hub lists
func (modelscopeAPI) Facets(ctx context.Context, c *Client) ([]*v1.Facet, error) {
	var tree struct {
		Data struct {
			Domains []struct {
				DomainName string   `json:"DomainName"`
				Tasks      []msTask `json:"Tasks"`
			} `json:"Domains"`
		} `json:"Data"`
	}
	if _, err := c.JSON(ctx, c.URL("api", "v1", "tasks"), nil, &tree); err != nil {
		return nil, err
	}
	// A subtask goes as parent:child, as the site names it
	task := Hint(NewFacet(FacetTask, "Task", false), "A task with subtasks takes in all of them")
	for _, d := range tree.Data.Domains {
		for _, t := range d.Tasks {
			if t.Name == "" {
				continue
			}
			task.Values = append(task.Values, GroupedValue(t.Name, "", d.DomainName))
			for _, sub := range t.Tasks {
				if sub.Name != "" {
					task.Values = append(task.Values, GroupedValue(t.Name+":"+sub.Name, Humanize(sub.Name), d.DomainName))
				}
			}
		}
	}
	root, err := msSearch(ctx, c, msQuery{PageNumber: 1, PageSize: 1, SortBy: "Default"})
	if err != nil {
		return nil, err
	}
	out := []*v1.Facet{task}
	for _, a := range msAggs {
		f := Hint(NewFacet(a.id, a.label, true), a.hint)
		for _, b := range root.Data.FiledAgg[a.agg] {
			if b.Value == "" || b.Value == "null" {
				continue
			}
			label := b.Value
			if a.name != nil {
				label = a.name(b.Value)
			}
			f.Values = append(f.Values, Value(b.Value, label))
		}
		out = append(out, f)
	}
	var orgs struct {
		msEnvelope
		Data struct {
			Suggests []struct {
				Name string `json:"Name"`
			} `json:"Suggests"`
		} `json:"Data"`
	}
	body := map[string]any{"PageSize": msOrganizations, "PageNumber": 1, "Query": ""}
	if _, err := c.JSONBody(ctx, http.MethodPut, c.URL("api", "v1", "dolphin", "organizations"), nil, body, &orgs); err != nil {
		return nil, err
	}
	if err := orgs.err("modelscope organizations"); err != nil {
		return nil, err
	}
	org := Hint(NewFacet(FacetPublisher, "Organization", true), "The organizations the hub lists first. Name any other as the owner.")
	for _, o := range orgs.Data.Suggests {
		if o.Name != "" {
			org.Values = append(org.Values, Value(o.Name, o.Name))
		}
	}
	return append(out, org), nil
}

// A search of the model list
type msQuery struct {
	Name            string        `json:"Name"`
	PageNumber      int           `json:"PageNumber"`
	PageSize        int           `json:"PageSize"`
	SortBy          string        `json:"SortBy"`
	Order           string        `json:"Order,omitempty"`
	Criterion       []msCriterion `json:"Criterion,omitempty"`
	SingleCriterion []msSingle    `json:"SingleCriterion,omitempty"`
}

// Models carrying one of the values under a category. Several criteria must all hold.
type msCriterion struct {
	Category  string   `json:"category"`
	Predicate string   `json:"predicate"`
	Values    []string `json:"values"`
	// Subtasks of the task in values, which then stand in for it
	SubValues []string `json:"sub_values,omitempty"`
}

// Models whose field equals a number or a word, the hub spelling the kind of value DateType
type msSingle struct {
	Category    string `json:"category"`
	Predicate   string `json:"predicate"`
	DateType    string `json:"DateType"`
	IntValue    int    `json:"IntValue,omitempty"`
	StringValue string `json:"StringValue,omitempty"`
}

// Models carrying any of the values under a category
func msContains(category string, values ...string) msCriterion {
	return msCriterion{Category: category, Predicate: "contains", Values: values}
}

type msSearchBody struct {
	msEnvelope
	Data struct {
		Model struct {
			TotalCount uint64           `json:"TotalCount"`
			Models     []map[string]any `json:"Models"`
		} `json:"Model"`
		// Value counts of the matching models by field, the hub's spelling
		FiledAgg map[string][]struct {
			Value string `json:"Value"`
		} `json:"FiledAgg"`
	} `json:"Data"`
}

// Runs one search, refusing an answer whose code reports a failure
func msSearch(ctx context.Context, c *Client, query msQuery) (*msSearchBody, error) {
	var root msSearchBody
	if _, err := c.JSONBody(ctx, http.MethodPut, c.URL("api", "v1", "dolphin", "models"), nil, query, &root); err != nil {
		return nil, err
	}
	if err := root.err("modelscope"); err != nil {
		return nil, err
	}
	return &root, nil
}

func (modelscopeAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	limit, page := c.Limit(req), c.page(req)
	query := msQuery{Name: strings.TrimSpace(req.GetQuery()), PageNumber: page, PageSize: limit, SortBy: sort.Key, Order: direction(sort.Ascending, "asc", "desc")}
	if err := msFilters(req, &query); err != nil {
		return nil, err
	}
	root, err := msSearch(ctx, c, query)
	if err != nil {
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

// Turns the request's filters into the hub's criteria
func msFilters(req *v1.SearchRequest, q *msQuery) error {
	if task := FilterOne(req, FacetTask); task != "" {
		parent, sub, _ := strings.Cut(task, ":")
		tasks := msContains("tasks", parent)
		if sub != "" {
			tasks.SubValues = []string{sub}
		}
		q.Criterion = append(q.Criterion, tasks)
	}
	for _, a := range msAggs {
		if values := Filter(req, a.id); len(values) > 0 {
			q.Criterion = append(q.Criterion, msContains(a.category, values...))
		}
	}
	if orgs := Filter(req, FacetPublisher); len(orgs) > 0 {
		q.Criterion = append(q.Criterion, msContains("organizations", orgs...))
	}
	// The hub matches a whole namespace here, a user's as well as an organization's
	if owner := Author(req); owner != "" {
		q.Criterion = append(q.Criterion, msContains("organizations", owner))
	}
	if bases := Filter(req, FacetBaseModel); len(bases) > 0 {
		q.Criterion = append(q.Criterion, msContains("base_model_no_ver", bases...))
	}
	if len(req.GetTags()) > 0 {
		q.Criterion = append(q.Criterion, msContains("tags", req.GetTags()...))
	}
	if relation := FilterOne(req, msFacetRelation); relation != "" {
		q.SingleCriterion = append(q.SingleCriterion, msSingle{Category: "base_model_relation", Predicate: "equal", DateType: "string", StringValue: relation})
	}
	if id := FilterOne(req, msFacetService); id != "" {
		i := slices.IndexFunc(msServices, func(s msService) bool { return s.id == id })
		switch {
		case i < 0:
			return fmt.Errorf("%w: %s %q is no service the hub runs", ErrSource, msFacetService, id)
		case msServices[i].pai != "":
			q.Criterion = append(q.Criterion, msContains("support_pai_model_gallery", msServices[i].pai))
		default:
			q.SingleCriterion = append(q.SingleCriterion, msSingle{Category: msServices[i].flag, Predicate: "equal", DateType: "int", IntValue: 1})
		}
	}
	return nil
}

// A count the hub sends, zero when missing or below zero as stars taken back can leave it
func msCount(v any) uint64 {
	if n, err := text.Number(v); err == nil && n > 0 {
		return uint64(n)
	}
	return 0
}

// Sets an extra to the values as a comma list, leaving it out when none is set
func msExtra(hit *v1.SearchHit, key string, values ...string) {
	if values = slices.DeleteFunc(values, func(s string) bool { return s == "" }); len(values) > 0 {
		hit.Extra[key] = strings.Join(values, ",")
	}
}

func msHit(c *Client, m map[string]any) *v1.SearchHit {
	owner, _ := m["Path"].(string)
	name, _ := m["Name"].(string)
	if owner == "" || name == "" {
		return nil
	}
	hit := newHit(owner+"/"+name, name, owner)
	hit.Url = msPage(c, hit.Repo)
	hit.Downloads, hit.Likes = msCount(m["Downloads"]), msCount(m["Stars"])
	// The size of every file at the default revision
	hit.SizeBytes = msCount(m["StorageSize"])
	// What the hub reads from the safetensors or GGUF headers
	infos, _ := m["ModelInfos"].(map[string]any)
	safetensor, _ := infos["safetensor"].(map[string]any)
	gguf, _ := infos["gguf"].(map[string]any)
	hit.Parameters = cmp.Or(msCount(safetensor["model_size"]), msCount(gguf["model_size"]))
	if n, err := text.Number(m["LastUpdatedTime"]); err == nil && n > 0 {
		hit.UpdatedAt = timestamppb.New(time.Unix(int64(n), 0))
	}
	if n, err := text.Number(m["CreatedTime"]); err == nil && n > 0 {
		hit.CreatedAt = timestamppb.New(time.Unix(int64(n), 0))
	}
	if n, err := text.Number(m["Visibility"]); err == nil && n != 0 && n != msPublicVisible {
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
	arch := text.Scalar(gguf["architecture"])
	if archs := msStrings(m["Architectures"]); len(archs) > 0 {
		arch = archs[0]
	}
	msExtra(hit, "architecture", arch)
	if cn, _ := m["ChineseName"].(string); cn != "" && cn != name {
		hit.Extra["title"] = cn
	}
	msExtra(hit, "model_type", msStrings(m["ModelType"])...)
	msExtra(hit, "base_model", msStrings(m["BaseModel"])...)
	msExtra(hit, "relation", text.Scalar(m["BaseModelRelation"]))
	msExtra(hit, "languages", msStrings(m["Language"])...)
	msExtra(hit, "dtypes", msStrings(safetensor["tensor_type"])...)
	msExtra(hit, "aigc_type", text.Scalar(m["AigcType"]))
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
		if s := text.Scalar(item); s != "" {
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

// How long one repository detail answers both the resolve and the card of a page view
const msDetailTTL = time.Minute

// The repository detail, which lists the same facts the search does beside its default revision
func msDetail(ctx context.Context, c *Client, repo string) (map[string]any, error) {
	memo := Cached(c, "modelscope-model:"+repo, func() *Memo[map[string]any] { return &Memo[map[string]any]{TTL: msDetailTTL} })
	return memo.Get(ctx, func(ctx context.Context) (map[string]any, error) {
		var resp struct {
			msEnvelope
			Data map[string]any `json:"Data"`
		}
		if _, err := c.JSON(ctx, c.URL("api", "v1", "models", repo), nil, &resp); err != nil {
			return nil, err
		}
		if err := resp.err(repo); err != nil {
			return nil, err
		}
		return resp.Data, nil
	})
}

// The revision the hub serves when none is named, as the repository detail reports it
func msDefault(ctx context.Context, c *Client, repo string) (string, error) {
	data, err := msDetail(ctx, c, repo)
	if err != nil {
		return "", err
	}
	revision, _ := data["Revision"].(string)
	if revision == "" {
		return "", fmt.Errorf("%s: modelscope names no default revision", repo)
	}
	return revision, nil
}

// Where a browser fetches one file of a repo at a revision
func msFileURL(c *Client, repo, revision, path string) string {
	q := url.Values{"Revision": {revision}, "FilePath": {path}}
	return c.URL("api", "v1", "models", repo, "repo") + "?" + q.Encode()
}

func (modelscopeAPI) Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error) {
	if revision == "" {
		var err error
		if revision, err = msDefault(ctx, c, repo); err != nil {
			return nil, err
		}
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
		model.Artifacts = append(model.Artifacts, &v1.Artifact{Path: f.Path, SizeBytes: f.Size, Sha256: Hex(f.Sha256), Url: msFileURL(c, repo, revision, f.Path)})
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
	def, err := msDefault(ctx, c, repo)
	if err != nil {
		return nil, err
	}
	var out []*v1.Revision
	add := func(e msRef, tag bool) {
		r := refRevision(e.Revision, "", e.Revision == def, tag)
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
		var err error
		if revision, err = msDefault(ctx, c, repo); err != nil {
			return nil, err
		}
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
	data, err := msDetail(ctx, c, repo)
	if err != nil {
		return nil, err
	}
	card.Hit = msHit(c, data)
	return card, nil
}

func (modelscopeAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	return c.Range(ctx, msFileURL(c, model.GetRepo(), model.GetRevision(), artifact.GetPath()), artifact)
}
