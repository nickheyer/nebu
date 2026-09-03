// Package local adopts model files already on disk.
package local

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	revision    = "local"
	searchDepth = 2
)

type source struct {
	spec *v1.Source
	root string
}

// Builds a local directory source from config
func New(cfg *v1.Source) (sources.Source, error) {
	if cfg.GetPath() == "" {
		return nil, fmt.Errorf("local source needs path")
	}
	root, err := filepath.Abs(cfg.GetPath())
	if err != nil {
		return nil, err
	}
	return &source{spec: cfg, root: root}, nil
}

func (s *source) Spec() *v1.Source { return s.spec }

func (s *source) Search(ctx context.Context, query string, tags []string, limit int) ([]*v1.SearchHit, error) {
	var hits []*v1.SearchHit
	err := filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == s.root {
			return nil
		}
		rel, _ := filepath.Rel(s.root, p)
		if strings.Count(rel, string(filepath.Separator)) >= searchDepth {
			return fs.SkipDir
		}
		if query == "" || strings.Contains(strings.ToLower(rel), strings.ToLower(query)) {
			info, ierr := d.Info()
			hit := &v1.SearchHit{SourceId: s.spec.GetId(), Repo: filepath.ToSlash(rel)}
			if ierr == nil {
				hit.UpdatedAt = timestamppb.New(info.ModTime())
			}
			hits = append(hits, hit)
			if limit > 0 && len(hits) >= limit {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return hits, err
}

func (s *source) dir(repo string) (string, error) {
	dir := filepath.Join(s.root, filepath.FromSlash(repo))
	if rel, err := filepath.Rel(s.root, dir); err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("repo %q escapes source root", repo)
	}
	return dir, nil
}

func (s *source) Resolve(ctx context.Context, repo, rev string) (*v1.Model, error) {
	dir, err := s.dir(repo)
	if err != nil {
		return nil, err
	}
	model := &v1.Model{SourceId: s.spec.GetId(), Repo: repo, Revision: revision, ResolvedAt: timestamppb.Now()}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		model.Artifacts = append(model.Artifacts, &v1.Artifact{Path: filepath.ToSlash(rel), SizeBytes: uint64(info.Size())})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return model, nil
}

func (s *source) Open(ctx context.Context, model *v1.Model, artifact *v1.Artifact) (sources.Blob, error) {
	dir, err := s.dir(model.GetRepo())
	if err != nil {
		return nil, err
	}
	return sources.OpenFile(filepath.Join(dir, filepath.FromSlash(artifact.GetPath())))
}
