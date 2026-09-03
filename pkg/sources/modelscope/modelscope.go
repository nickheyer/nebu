// Package modelscope talks to the ModelScope hub API.
package modelscope

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultEndpoint = "https://www.modelscope.cn"
	defaultTokenEnv = "MODELSCOPE_API_TOKEN"
	defaultRevision = "master"
	defaultLimit    = 20
)

type source struct {
	spec   *v1.Source
	client *sources.Client
	base   string
}

// Builds a ModelScope source from config
func New(cfg *v1.Source) (sources.Source, error) {
	endpoint := cfg.GetEndpoint()
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	tokenEnv := cfg.GetTokenEnv()
	if tokenEnv == "" {
		tokenEnv = defaultTokenEnv
	}
	client, err := sources.NewClient(endpoint, os.Getenv(tokenEnv))
	if err != nil {
		return nil, err
	}
	return &source{spec: cfg, client: client, base: strings.TrimRight(endpoint, "/")}, nil
}

func (s *source) Spec() *v1.Source { return s.spec }

func (s *source) Search(ctx context.Context, query string, tags []string, limit int) ([]*v1.SearchHit, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	body, _ := json.Marshal(map[string]any{"Name": query, "PageNumber": 1, "PageSize": limit, "SortBy": "Default", "Criterion": tagCriteria(tags)})
	var root map[string]any
	if err := s.put(ctx, s.base+"/api/v1/dolphin/models", body, &root); err != nil {
		return nil, err
	}
	data, _ := root["Data"].(map[string]any)
	model, _ := data["Model"].(map[string]any)
	items, _ := model["Models"].([]any)
	hits := make([]*v1.SearchHit, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		owner, _ := m["Path"].(string)
		name, _ := m["Name"].(string)
		if owner == "" || name == "" {
			continue
		}
		hit := &v1.SearchHit{SourceId: s.spec.GetId(), Repo: owner + "/" + name, Author: owner}
		if n, err := eval.Number(m["Downloads"]); err == nil {
			hit.Downloads = uint64(n)
		}
		if n, err := eval.Number(m["Stars"]); err == nil {
			hit.Likes = uint64(n)
		}
		if n, err := eval.Number(m["LastUpdatedTime"]); err == nil && n > 0 {
			hit.UpdatedAt = timestamppb.New(time.Unix(int64(n), 0))
		}
		if tags, ok := m["Tags"].([]any); ok {
			for _, t := range tags {
				hit.Tags = append(hit.Tags, eval.Scalar(t))
			}
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

func tagCriteria(tags []string) []map[string]any {
	var out []map[string]any
	for _, t := range tags {
		out = append(out, map[string]any{"category": "tags", "predicate": "contains", "values": []string{t}})
	}
	return out
}

type fileEntry struct {
	Path     string `json:"Path"`
	Name     string `json:"Name"`
	Size     uint64 `json:"Size"`
	Sha256   string `json:"Sha256"`
	Type     string `json:"Type"`
	Revision string `json:"Revision"`
}

type filesResponse struct {
	Code int `json:"Code"`
	Data struct {
		Files []fileEntry `json:"Files"`
	} `json:"Data"`
	Message string `json:"Message"`
}

func (s *source) Resolve(ctx context.Context, repo, revision string) (*v1.Model, error) {
	if revision == "" {
		revision = defaultRevision
	}
	var resp filesResponse
	q := url.Values{"Revision": {revision}, "Recursive": {"true"}}
	if _, err := s.client.JSON(ctx, s.client.URL("api", "v1", "models", repo, "repo", "files"), q, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 0 && resp.Code != 200 {
		return nil, fmt.Errorf("%s: %s", repo, resp.Message)
	}
	model := &v1.Model{SourceId: s.spec.GetId(), Repo: repo, Revision: revision, ResolvedAt: timestamppb.Now()}
	revisions := map[string]bool{}
	h := sha256.New()
	for _, f := range resp.Data.Files {
		if f.Type != "blob" {
			continue
		}
		model.Artifacts = append(model.Artifacts, &v1.Artifact{Path: f.Path, SizeBytes: f.Size, Sha256: strings.ToLower(f.Sha256)})
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

func (s *source) Open(ctx context.Context, model *v1.Model, artifact *v1.Artifact) (sources.Blob, error) {
	if artifact.GetSizeBytes() == 0 {
		return nil, fmt.Errorf("%s: unknown size", artifact.GetPath())
	}
	q := url.Values{"Revision": {model.GetRevision()}, "FilePath": {artifact.GetPath()}}
	rawURL := s.client.URL("api", "v1", "models", model.GetRepo(), "repo") + "?" + q.Encode()
	return sources.NewRangeBlob(s.client, rawURL, int64(artifact.GetSizeBytes())), nil
}

// Sends a JSON body with PUT, as search requires
func (s *source) put(ctx context.Context, rawURL string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Send(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("PUT %s: %s", rawURL, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
