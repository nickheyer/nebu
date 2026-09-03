// Package mirror reads exported stores over HTTP, S3, or directories.
package mirror

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/nickheyer/nebu/pkg/mirror"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	revision     = "mirror"
	defaultLimit = 20
)

type source struct {
	spec   *v1.Source
	client *sources.Client
	base   string
	dir    string
}

// Builds a mirror source from an endpoint or a directory
func New(cfg *v1.Source) (sources.Source, error) {
	s := &source{spec: cfg}
	switch {
	case cfg.GetPath() != "":
		dir, err := filepath.Abs(cfg.GetPath())
		if err != nil {
			return nil, err
		}
		s.dir = dir
	case cfg.GetEndpoint() != "":
		client, err := sources.NewClient(cfg.GetEndpoint(), os.Getenv(cfg.GetTokenEnv()))
		if err != nil {
			return nil, err
		}
		s.client, s.base = client, strings.TrimRight(cfg.GetEndpoint(), "/")
	default:
		return nil, fmt.Errorf("mirror source needs endpoint or path")
	}
	return s, nil
}

func (s *source) Spec() *v1.Source { return s.spec }

func (s *source) Search(ctx context.Context, query string, tags []string, limit int) ([]*v1.SearchHit, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	var root mirror.Root
	if err := s.readJSON(ctx, mirror.IndexFile, &root); err != nil {
		repos, lerr := s.listPrefixes(ctx)
		if lerr != nil {
			return nil, err
		}
		for _, r := range repos {
			root.Repos = append(root.Repos, mirror.RepoEntry{Repo: r})
		}
	}
	var hits []*v1.SearchHit
	for _, r := range root.Repos {
		if query != "" && !strings.Contains(strings.ToLower(r.Repo), strings.ToLower(query)) {
			continue
		}
		hit := &v1.SearchHit{SourceId: s.spec.GetId(), Repo: r.Repo}
		if !r.UpdatedAt.IsZero() {
			hit.UpdatedAt = timestamppb.New(r.UpdatedAt)
		}
		hits = append(hits, hit)
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

func (s *source) Resolve(ctx context.Context, repo, rev string) (*v1.Model, error) {
	if strings.Contains(repo, "..") {
		return nil, fmt.Errorf("repo %q escapes the mirror", repo)
	}
	model := &v1.Model{SourceId: s.spec.GetId(), Repo: repo, Revision: revision, ResolvedAt: timestamppb.Now()}
	var index mirror.Index
	if err := s.readJSON(ctx, path.Join(repo, mirror.IndexFile), &index); err == nil {
		model.Commit = index.Commit
		if index.Revision != "" {
			model.Revision = index.Revision
		}
		for _, f := range index.Files {
			model.Artifacts = append(model.Artifacts, &v1.Artifact{Path: f.Path, SizeBytes: f.Size, Sha256: strings.ToLower(f.Sha256)})
		}
		return model, nil
	}
	files, err := s.listObjects(ctx, repo)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		model.Artifacts = append(model.Artifacts, &v1.Artifact{Path: f.Path, SizeBytes: f.Size})
	}
	if len(model.Artifacts) == 0 {
		return nil, fmt.Errorf("%s: no index and no objects in the mirror", repo)
	}
	return model, nil
}

func (s *source) Open(ctx context.Context, model *v1.Model, artifact *v1.Artifact) (sources.Blob, error) {
	rel := path.Join(model.GetRepo(), artifact.GetPath())
	if s.dir != "" {
		full := filepath.Join(s.dir, filepath.FromSlash(rel))
		if r, err := filepath.Rel(s.dir, full); err != nil || strings.HasPrefix(r, "..") {
			return nil, fmt.Errorf("%s escapes the mirror", rel)
		}
		return sources.OpenFile(full)
	}
	if artifact.GetSizeBytes() == 0 {
		return nil, fmt.Errorf("%s: unknown size", artifact.GetPath())
	}
	return sources.NewRangeBlob(s.client, s.client.URL(rel), int64(artifact.GetSizeBytes())), nil
}

func (s *source) readJSON(ctx context.Context, rel string, out any) error {
	if s.dir != "" {
		data, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		return json.Unmarshal(data, out)
	}
	_, err := s.client.JSON(ctx, s.client.URL(rel), nil, out)
	return err
}

// One object from an S3 style listing
type object struct {
	Path string
	Size uint64
}

type listResult struct {
	XMLName               xml.Name `xml:"ListBucketResult"`
	IsTruncated           bool     `xml:"IsTruncated"`
	NextContinuationToken string   `xml:"NextContinuationToken"`
	Contents              []struct {
		Key  string `xml:"Key"`
		Size uint64 `xml:"Size"`
	} `xml:"Contents"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

// Lists objects under a repo through ListObjectsV2, or a directory
func (s *source) listObjects(ctx context.Context, repo string) ([]object, error) {
	if s.dir != "" {
		var out []object
		root := filepath.Join(s.dir, filepath.FromSlash(repo))
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() == mirror.IndexFile {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			out = append(out, object{Path: filepath.ToSlash(rel), Size: uint64(info.Size())})
			return nil
		})
		return out, err
	}
	var out []object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {repo + "/"}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		var res listResult
		if err := s.readXML(ctx, q, &res); err != nil {
			return nil, err
		}
		for _, c := range res.Contents {
			rel := strings.TrimPrefix(c.Key, repo+"/")
			if rel == "" || strings.HasSuffix(rel, "/") || path.Base(rel) == mirror.IndexFile {
				continue
			}
			out = append(out, object{Path: rel, Size: c.Size})
		}
		if !res.IsTruncated || res.NextContinuationToken == "" {
			return out, nil
		}
		token = res.NextContinuationToken
	}
}

// Lists repositories as two level prefixes, or directories
func (s *source) listPrefixes(ctx context.Context) ([]string, error) {
	if s.dir != "" {
		var out []string
		owners, err := os.ReadDir(s.dir)
		if err != nil {
			return nil, err
		}
		for _, o := range owners {
			if !o.IsDir() {
				continue
			}
			names, err := os.ReadDir(filepath.Join(s.dir, o.Name()))
			if err != nil {
				continue
			}
			for _, n := range names {
				if n.IsDir() {
					out = append(out, o.Name()+"/"+n.Name())
				}
			}
		}
		return out, nil
	}
	var owners listResult
	if err := s.readXML(ctx, url.Values{"list-type": {"2"}, "delimiter": {"/"}}, &owners); err != nil {
		return nil, err
	}
	var out []string
	for _, o := range owners.CommonPrefixes {
		var names listResult
		if err := s.readXML(ctx, url.Values{"list-type": {"2"}, "delimiter": {"/"}, "prefix": {o.Prefix}}, &names); err != nil {
			return nil, err
		}
		for _, n := range names.CommonPrefixes {
			out = append(out, strings.TrimSuffix(n.Prefix, "/"))
		}
	}
	return out, nil
}

func (s *source) readXML(ctx context.Context, q url.Values, out any) error {
	resp, err := s.client.Do(ctx, http.MethodGet, s.base+"/", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return xml.NewDecoder(resp.Body).Decode(out)
}
