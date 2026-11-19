package sources

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/sync/errgroup"
)

// OpenCSG, the public CSGHub
var csghub = &Catalog{
	ID:            "csghub",
	Kind:          v1.SourceKind_SOURCE_KIND_CSGHUB,
	Name:          "CSGHub",
	Seed:          "OpenCSG",
	Transports:    []Use{httpUse("https://hub.opencsg.com", "OPENCSG_TOKEN")},
	Web:           "https://opencsg.com",
	WebPath:       "/models",
	Description:   "OpenCSG, the CSGHub community hub, or any CSGHub install",
	RepoExample:   "org/model",
	RepoPattern:   `^[\w.-]+/[\w.-]+$`,
	RevisionLabel: "revision",
	// Every order runs one way, and a search ranks by its own match whatever the sort. The hub also
	// takes most_star, which orders models by a star count only code and MCP repositories keep.
	Sorts: []string{SortTrending, SortDownloads, SortLikes, SortUpdated},
	SortKeys: map[string]string{
		SortTrending:  "trending",
		SortDownloads: "most_download",
		SortLikes:     "most_favorite",
		SortUpdated:   "recently_update",
	},
	// Filters the hub takes as parameters of their own, after the facets its tags fill
	Facets: []*v1.Facet{
		Hint(Between(csgFacetParameters, "Parameters", ParameterSpans()...), "Parameter count, such as 1B to 8B"),
		Hint(NewFacet(csgFacetReview, "License review", false, csgReviews...), "Where the hub's check of the model's license stands"),
		Hint(Toggle(csgFacetServerless, "Serverless"), "Models the hub serves from a running serverless endpoint"),
		Hint(Multi(Freeform(FacetTag, "Tag")), "Models carrying every tag named, of any category"),
	},
	Noise: []string{".+:.+", "[a-z]{2,3}"},
	// The list leaves out the metadata a repository's own page carries, its size, parameters,
	// architecture, and base model among it
	Columns: slices.Concat(
		Shown(Cols(ColDownloads, ColLikes, ColUpdated)...),
		Cols(ColCreated, ColTask),
		[]*v1.HitColumn{Named(Col(ColLibrary), "Framework")},
		Cols(ColLicense, ColAuthor, ColDescription),
		[]*v1.HitColumn{
			ExtraCol("runtime_framework", "Runtime frameworks", AsList, "Inference, fine-tuning, and evaluation frameworks the hub runs the model with"),
			ExtraCol("languages", "Languages", AsList, "Languages the model is tagged with"),
			ExtraCol("commercial", "Commercial use", AsText, "What the model's license allows commercially, as the hub reads it"),
			ExtraCol("review", "License review", AsText, "Where the hub's check of the model's license stands"),
			ExtraCol("hf_path", "Hugging Face", AsText, "The Hugging Face repository the model mirrors"),
		},
	),
	API: csghubAPI{},
}

func init() { register(csghub) }

const (
	csgRevision = "main"
	csgMaxDepth = 8
	// Directories listed for one model, how many at once, how long one listing may take, and how
	// long the walk keeps starting new ones
	csgMaxDirs     = 128
	csgWalkWorkers = 8
	csgDirTimeout  = 10 * time.Second
	csgWalkBudget  = 25 * time.Second
)

// Facets only CSGHub offers
const (
	csgFacetDomain     = "domain"
	csgFacetLanguage   = "language"
	csgFacetRuntime    = "runtime_framework"
	csgFacetResource   = "resource"
	csgFacetParameters = "parameters"
	csgFacetReview     = "review"
	csgFacetServerless = "serverless"
)

// A facet filled from one category of the hub's built in tags
type csgCategory struct {
	category, id, label, hint string
	// Offers the groups the category's tags fall in rather than the tags
	groups bool
}

// Tag categories the hub filters models by, by the facet each fills. It stores its industry tags
// capitalised and matches the name lower cased, so an industry filter finds none of them.
var csgTaxonomy = []csgCategory{
	{category: "task", id: FacetTask, label: "Task"},
	{category: "task", id: csgFacetDomain, label: "Domain", hint: "Every task of one field", groups: true},
	{category: "framework", id: FacetFramework, label: "Framework"},
	{category: "license", id: FacetLicense, label: "License"},
	{category: "language", id: csgFacetLanguage, label: "Language"},
	{category: "runtime_framework", id: csgFacetRuntime, label: "Runtime framework", hint: "Inference, fine-tuning, and evaluation frameworks the hub runs the model with"},
	{category: "resource", id: csgFacetResource, label: "Hardware", hint: "Accelerators the hub runs the model on"},
}

// License review states as the hub's source names them, checked before sending since the hub
// answers a state it does not know unfiltered
var csgReviews = []*v1.FacetValue{Value("compliant", ""), Value("pending_review", ""), Value("non_compliant", "")}

// The CSGHub API: enveloped JSON, a paged list, branches, a tree, and Hub style downloads
type csghubAPI struct{}

// The hub wraps every answer in a data field
type csgEnvelope[T any] struct {
	Data T `json:"data"`
}

// Fetches one enveloped answer
func csgData[T any](ctx context.Context, c *Client, rawURL string, q url.Values) (T, error) {
	var body csgEnvelope[T]
	_, err := c.JSON(ctx, rawURL, q, &body)
	return body.Data, err
}

type csgTag struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Group    string `json:"group"`
	BuiltIn  bool   `json:"built_in"`
	ShowName string `json:"show_name"`
}

// Reads the hub's own tag taxonomy, keeping the built in entries. The hub matches a tag's name
// lower cased, so names differing only in case are one value.
func (csghubAPI) Facets(ctx context.Context, c *Client) ([]*v1.Facet, error) {
	tags, err := csgData[[]csgTag](ctx, c, c.URL("api", "v1", "tags"), url.Values{"scope": {"model"}, "built_in": {"true"}})
	if err != nil {
		return nil, err
	}
	out := make([]*v1.Facet, 0, len(csgTaxonomy))
	for _, cat := range csgTaxonomy {
		f := Hint(NewFacet(cat.id, cat.label, false), cat.hint)
		seen := map[string]bool{}
		for _, e := range tags {
			id, label, group := strings.ToLower(e.Name), cmp.Or(e.ShowName, e.Name), Humanize(e.Group)
			if cat.groups {
				id, label, group = e.Group, "", ""
			}
			if !e.BuiltIn || e.Category != cat.category || id == "" || seen[id] {
				continue
			}
			seen[id] = true
			f.Values = append(f.Values, GroupedValue(id, label, group))
		}
		out = append(out, f)
	}
	return out, nil
}

type csgModel struct {
	Name        string `json:"name"`
	Nickname    string `json:"nickname"`
	Description string `json:"description"`
	Likes       int64  `json:"likes"`
	Downloads   int64  `json:"downloads"`
	Path        string `json:"path"`
	Private     bool   `json:"private"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	Tags        []struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		BuiltIn  bool   `json:"built_in"`
	} `json:"tags"`
	License              string `json:"license"`
	ComplianceStatus     string `json:"compliance_status"`
	CommercialPermission string `json:"commercial_permission"`
	BaseModel            string `json:"base_model"`
	HFPath               string `json:"hf_path"`
	DefaultBranch        string `json:"default_branch"`
	Metadata             struct {
		ModelParams  float64 `json:"model_params"`
		Architecture string  `json:"architecture"`
	} `json:"metadata"`
}

func (csghubAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	limit, page := c.Limit(req), c.page(req)
	q := url.Values{"sort": {sort.Key}, "per": {strconv.Itoa(limit)}, "page": {strconv.Itoa(page)}}
	if query := strings.TrimSpace(req.GetQuery()); query != "" {
		q.Set("search", query)
	}
	// Tags go as category, name, and group, the three lists kept in step. A model must carry every
	// tag named, and a blank part matches any.
	tag := func(category, name, group string) {
		q.Add("tag_category", category)
		q.Add("tag_name", name)
		q.Add("tag_group", group)
	}
	for _, cat := range csgTaxonomy {
		switch v := FilterOne(req, cat.id); {
		case v == "":
		case cat.groups:
			tag(cat.category, "", v)
		default:
			tag(cat.category, v, "")
		}
	}
	for _, t := range append(Filter(req, FacetTag), req.GetTags()...) {
		tag("", t, "")
	}
	if err := csgFilters(req, q); err != nil {
		return nil, err
	}
	var body struct {
		Total uint64     `json:"total"`
		Data  []csgModel `json:"data"`
	}
	if _, err := c.JSON(ctx, c.URL("api", "v1", "models"), q, &body); err != nil {
		return nil, err
	}
	resp := &v1.SearchResponse{Hits: make([]*v1.SearchHit, 0, len(body.Data)), Total: body.Total}
	for _, m := range body.Data {
		if hit := csgHit(c, m); hit != nil {
			resp.Hits = append(resp.Hits, hit)
		}
	}
	c.nextPage(resp, page, limit, body.Total)
	return resp, nil
}

// Adds the filters the hub takes as parameters of their own rather than as tags.
func csgFilters(req *v1.SearchRequest, q url.Values) error {
	// The model list takes no owner and would answer unfiltered
	if Author(req) != "" {
		return fmt.Errorf("author: %w", ErrUnsupported)
	}
	bounds, err := Range(req, csgFacetParameters)
	if err != nil {
		return err
	}
	// The hub counts parameters in billions
	for param, side := range map[string]*uint64{"model_params_min": bounds.Min, "model_params_max": bounds.Max} {
		if side != nil {
			q.Set(param, strconv.FormatFloat(float64(*side)/1e9, 'f', -1, 64))
		}
	}
	if review := FilterOne(req, csgFacetReview); review != "" {
		if !slices.ContainsFunc(csgReviews, func(v *v1.FacetValue) bool { return v.GetId() == review }) {
			return fmt.Errorf("%w: %s %q is no state the hub reviews licenses to", ErrSource, csgFacetReview, review)
		}
		q.Set("compliance_status", review)
	}
	if On(req, csgFacetServerless) {
		q.Set("list_serverless", "true")
	}
	return nil
}

func csgHit(c *Client, m csgModel) *v1.SearchHit {
	if m.Path == "" {
		return nil
	}
	author, name := splitRepo(m.Path)
	if m.Name != "" {
		name = m.Name
	}
	if m.Nickname != "" {
		name = m.Nickname
	}
	hit := newHit(m.Path, name, author)
	hit.Description, hit.License, hit.Private = Summary(m.Description), m.License, m.Private
	hit.Url = csgPage(c, m.Path)
	hit.CreatedAt, hit.UpdatedAt = Stamp(m.CreatedAt), Stamp(m.UpdatedAt)
	if m.Downloads > 0 {
		hit.Downloads = uint64(m.Downloads)
	}
	if m.Likes > 0 {
		hit.Likes = uint64(m.Likes)
	}
	var runtimes, languages []string
	builtInTask := false
	for _, t := range m.Tags {
		if t.Name == "" {
			continue
		}
		hit.Tags = append(hit.Tags, t.Name)
		switch t.Category {
		case "task":
			// The hub files free labels as tasks too, so one of its own tasks names the model's
			if hit.Task == "" || t.BuiltIn && !builtInTask {
				hit.Task, builtInTask = t.Name, t.BuiltIn
			}
		case "framework":
			if hit.Library == "" {
				hit.Library = t.Name
			}
		case "runtime_framework":
			runtimes = append(runtimes, t.Name)
		case "language":
			languages = append(languages, t.Name)
		}
	}
	if len(runtimes) > 0 {
		hit.Extra["runtime_framework"] = strings.Join(runtimes, ",")
	}
	if len(languages) > 0 {
		hit.Extra["languages"] = strings.Join(languages, ",")
	}
	if m.CommercialPermission != "" {
		hit.Extra["commercial"] = Humanize(m.CommercialPermission)
	}
	if m.ComplianceStatus != "" {
		hit.Extra["review"] = Humanize(m.ComplianceStatus)
	}
	if m.Metadata.ModelParams > 0 {
		hit.Parameters = csgParameters(m.Metadata.ModelParams)
	}
	if m.Metadata.Architecture != "" {
		hit.Extra["architecture"] = m.Metadata.Architecture
	}
	if m.HFPath != "" {
		hit.Extra["hf_path"] = m.HFPath
	}
	if m.BaseModel != "" {
		hit.Extra["base_model"] = m.BaseModel
	}
	return hit
}

// The model page on the site people browse
func csgPage(c *Client, repo string) string { return c.Page("models", repo) }

// The hub reports model_params in billions, a large value is already a count
func csgParameters(v float64) uint64 {
	if v < 1e6 {
		v *= 1e9
	}
	return uint64(v + 0.5)
}

type csgBranch struct {
	Name   string `json:"name"`
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
}

type csgTree struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Size      uint64 `json:"size"`
	Path      string `json:"path"`
	Lfs       bool   `json:"lfs"`
	LfsSha256 string `json:"lfs_sha256"`
}

func csgBranches(ctx context.Context, c *Client, repo string) ([]csgBranch, error) {
	return csgData[[]csgBranch](ctx, c, c.URL("api", "v1", "models", repo, "branches"), nil)
}

// Reads the default branch from the repo detail, main when the hub does not say
func csgDefaultBranch(ctx context.Context, c *Client, repo string) string {
	m, err := csgData[csgModel](ctx, c, c.URL("api", "v1", "models", repo), nil)
	if err != nil || m.DefaultBranch == "" {
		return csgRevision
	}
	return m.DefaultBranch
}

func (csghubAPI) Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error) {
	if revision == "" {
		revision = csgDefaultBranch(ctx, c, repo)
	}
	branches, err := csgBranches(ctx, c, repo)
	if err != nil {
		return nil, err
	}
	model := &v1.Model{Repo: repo, Revision: revision}
	for _, b := range branches {
		if b.Name == revision {
			model.Commit = b.Commit.ID
			break
		}
	}
	if err := csgWalk(ctx, c, repo, revision, model); err != nil {
		return nil, err
	}
	return model, nil
}

// Lists the tree of a revision breadth first, a level of directories at a time, within a depth, a
// count of directories, and a time budget. Directories the hub cannot list and any beyond those
// limits are reported on the model.
func csgWalk(ctx context.Context, c *Client, repo, revision string, model *v1.Model) error {
	type dir struct {
		path  string
		depth int
	}
	var mu sync.Mutex
	listed, pastCap, pastDepth, pastTime := 0, 0, 0, 0
	deadline := time.Now().Add(csgWalkBudget)
	level := []dir{{"", 0}}
	for len(level) > 0 {
		if time.Now().After(deadline) {
			pastTime += len(level)
			break
		}
		sort.Slice(level, func(i, j int) bool { return level[i].path < level[j].path })
		var next []dir
		eg, gctx := errgroup.WithContext(ctx)
		eg.SetLimit(csgWalkWorkers)
		for _, d := range level {
			if listed >= csgMaxDirs {
				pastCap++
				continue
			}
			listed++
			eg.Go(func() error {
				q := url.Values{"ref": {revision}}
				if d.path != "" {
					q.Set("path", d.path)
				}
				dctx, cancel := context.WithTimeout(gctx, csgDirTimeout)
				defer cancel()
				entries, err := csgData[[]csgTree](dctx, c, c.URL("api", "v1", "models", repo, "tree"), q)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					// The hub's git backend answers with a server error, or hangs, for directories it cannot open
					var se *StatusError
					switch {
					case errors.As(err, &se) && se.Code >= http.StatusInternalServerError:
						model.Warnings = append(model.Warnings, fmt.Sprintf("%s: not listed, the hub answered %s", d.path, se.Status))
					case errors.Is(err, context.DeadlineExceeded) && gctx.Err() == nil:
						model.Warnings = append(model.Warnings, fmt.Sprintf("%s: not listed, the hub took longer than %s", d.path, csgDirTimeout))
					default:
						return err
					}
					return nil
				}
				for _, e := range entries {
					path := e.Path
					if path == "" {
						path = strings.TrimPrefix(d.path+"/"+e.Name, "/")
					}
					switch e.Type {
					case "file":
						a := &v1.Artifact{Path: path, SizeBytes: e.Size, Url: c.URL("hf", repo, "resolve", revision, path)}
						if e.Lfs {
							a.Sha256 = Hex(e.LfsSha256)
						}
						model.Artifacts = append(model.Artifacts, a)
					case "dir", "directory":
						if d.depth+1 > csgMaxDepth {
							pastDepth++
							continue
						}
						next = append(next, dir{path, d.depth + 1})
					}
				}
				return nil
			})
		}
		if err := eg.Wait(); err != nil {
			return err
		}
		level = next
	}
	sort.Slice(model.Artifacts, func(i, j int) bool { return model.Artifacts[i].Path < model.Artifacts[j].Path })
	if pastDepth > 0 {
		model.Warnings = append(model.Warnings, fmt.Sprintf("%d directories deeper than %d levels not listed", pastDepth, csgMaxDepth))
	}
	if pastCap > 0 {
		model.Warnings = append(model.Warnings, fmt.Sprintf("%d directories not listed, %d is the most read for one model", pastCap, csgMaxDirs))
	}
	if pastTime > 0 {
		model.Warnings = append(model.Warnings, fmt.Sprintf("%d directories not listed, the listing stopped after %s", pastTime, csgWalkBudget))
	}
	return nil
}

func (csghubAPI) Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error) {
	branches, err := csgBranches(ctx, c, repo)
	if err != nil {
		return nil, err
	}
	def := csgDefaultBranch(ctx, c, repo)
	out := make([]*v1.Revision, 0, len(branches))
	for _, b := range branches {
		out = append(out, refRevision(b.Name, b.Commit.ID, b.Name == def, false))
	}
	return out, nil
}

func (csghubAPI) Card(ctx context.Context, c *Client, repo, revision string) (*v1.ModelCard, error) {
	var q url.Values
	if revision != "" {
		q = url.Values{"ref": {revision}}
	}
	card, err := c.CardText(ctx, c.URL("api", "v1", "models", repo, "raw", "README.md"), q, csgPage(c, repo))
	if err != nil || card.Markdown == "" {
		return card, err
	}
	// The raw file arrives inside the hub's JSON envelope
	var body csgEnvelope[string]
	if err := json.Unmarshal([]byte(card.Markdown), &body); err != nil {
		return nil, fmt.Errorf("decode %s card: %w", repo, err)
	}
	card.Markdown = body.Data
	m, err := csgData[csgModel](ctx, c, c.URL("api", "v1", "models", repo), nil)
	if err != nil {
		return nil, err
	}
	card.Hit = csgHit(c, m)
	return card, nil
}

func (csghubAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	revision := model.GetRevision()
	if revision == "" {
		revision = csgRevision
	}
	return c.Range(ctx, c.URL("hf", model.GetRepo(), "resolve", revision, artifact.GetPath()), artifact)
}
