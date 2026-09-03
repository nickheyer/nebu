// Package huggingface talks to the Hugging Face Hub API and compatible mirrors.
package huggingface

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultEndpoint = "https://huggingface.co"
	defaultTokenEnv = "HF_TOKEN"
	defaultRevision = "main"
	defaultLimit    = 20
)

type source struct {
	spec   *v1.Source
	client *sources.Client
}

// Builds a hub source from config
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
	return &source{spec: cfg, client: client}, nil
}

func (s *source) Spec() *v1.Source { return s.spec }

type searchItem struct {
	ID           string    `json:"id"`
	Author       string    `json:"author"`
	Downloads    uint64    `json:"downloads"`
	Likes        uint64    `json:"likes"`
	LastModified time.Time `json:"lastModified"`
	CreatedAt    time.Time `json:"createdAt"`
	Tags         []string  `json:"tags"`
}

func (s *source) Search(ctx context.Context, query string, tags []string, limit int) ([]*v1.SearchHit, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	q := url.Values{
		"search":    {query},
		"limit":     {strconv.Itoa(limit)},
		"sort":      {"downloads"},
		"direction": {"-1"},
	}
	for _, t := range tags {
		q.Add("filter", t)
	}
	var items []searchItem
	if _, err := s.client.JSON(ctx, s.client.URL("api", "models"), q, &items); err != nil {
		return nil, err
	}
	hits := make([]*v1.SearchHit, 0, len(items))
	for _, it := range items {
		author := it.Author
		if author == "" {
			author, _, _ = strings.Cut(it.ID, "/")
		}
		hit := &v1.SearchHit{
			SourceId:  s.spec.GetId(),
			Repo:      it.ID,
			Author:    author,
			Downloads: it.Downloads,
			Likes:     it.Likes,
			Tags:      it.Tags,
		}
		for _, t := range []time.Time{it.LastModified, it.CreatedAt} {
			if !t.IsZero() {
				hit.UpdatedAt = timestamppb.New(t)
				break
			}
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

type revisionInfo struct {
	Sha string `json:"sha"`
}

type treeEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size uint64 `json:"size"`
	Lfs  *struct {
		Oid  string `json:"oid"`
		Size uint64 `json:"size"`
	} `json:"lfs"`
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func (s *source) Resolve(ctx context.Context, repo, revision string) (*v1.Model, error) {
	if revision == "" {
		revision = defaultRevision
	}
	var info revisionInfo
	if _, err := s.client.JSON(ctx, s.client.URL("api", "models", repo, "revision", revision), nil, &info); err != nil {
		return nil, err
	}
	model := &v1.Model{
		SourceId:   s.spec.GetId(),
		Repo:       repo,
		Revision:   revision,
		Commit:     info.Sha,
		ResolvedAt: timestamppb.Now(),
	}
	next := s.client.URL("api", "models", repo, "tree", revision)
	query := url.Values{"recursive": {"true"}}
	for next != "" {
		var entries []treeEntry
		header, err := s.client.JSON(ctx, next, query, &entries)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Type != "file" {
				continue
			}
			a := &v1.Artifact{Path: e.Path, SizeBytes: e.Size}
			if e.Lfs != nil {
				a.Sha256 = e.Lfs.Oid
				a.SizeBytes = e.Lfs.Size
			}
			model.Artifacts = append(model.Artifacts, a)
		}
		next, query = "", nil
		if m := nextLink.FindStringSubmatch(header.Get("Link")); m != nil {
			next = m[1]
		}
	}
	return model, nil
}

func (s *source) Open(ctx context.Context, model *v1.Model, artifact *v1.Artifact) (sources.Blob, error) {
	if artifact.GetSizeBytes() == 0 {
		return nil, fmt.Errorf("%s: unknown size", artifact.GetPath())
	}
	rawURL := s.client.URL(model.GetRepo(), "resolve", model.GetRevision(), artifact.GetPath())
	return sources.NewRangeBlob(s.client, rawURL, int64(artifact.GetSizeBytes())), nil
}
