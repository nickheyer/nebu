package sources

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Model directories already on this host, adopted without copying; config names each one
var local = &Catalog{
	ID:            "local",
	Kind:          v1.SourceKind_SOURCE_KIND_LOCAL,
	Configured:    true,
	Description:   "Model directories already on this host, adopted without copying",
	RepoExample:   "org/model",
	RepoPattern:   `^[^/]+(/[^/]+)?$`,
	RevisionLabel: "revision",
	Sorts:         []string{SortName, SortUpdated},
	Reversible:    []string{SortName, SortUpdated},
	DefaultLimit:  50,
	MaxLimit:      500,
	API:           localAPI{},
}

func init() { register(local) }

const (
	locRevision    = "local"
	locSearchDepth = 2
)

// A directory of model directories: owner/name two levels deep, files read in place
type localAPI struct{}

// Needs a directory from config
func (localAPI) Check(c *Client) error {
	if c.Spec().GetPath() == "" {
		return fmt.Errorf("local source needs path")
	}
	_, err := locRoot(c)
	return err
}

func locRoot(c *Client) (string, error) {
	return filepath.Abs(c.Spec().GetPath())
}

func (localAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	root, err := locRoot(c)
	if err != nil {
		return nil, err
	}
	var hits []*v1.SearchHit
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if strings.Count(rel, string(filepath.Separator)) >= locSearchDepth {
			return fs.SkipDir
		}
		if strings.HasPrefix(d.Name(), ".") {
			return fs.SkipDir
		}
		// A directory holding files is a repo, a directory of only directories is an owner
		depth := strings.Count(rel, string(filepath.Separator))
		if depth < locSearchDepth-1 && !locHoldsFiles(p) {
			return nil
		}
		repo := filepath.ToSlash(rel)
		author, name, _ := strings.Cut(repo, "/")
		hit := &v1.SearchHit{Repo: repo, Name: name, Author: author}
		if name == "" {
			hit.Name, hit.Author = repo, ""
		}
		if info, ierr := d.Info(); ierr == nil {
			hit.UpdatedAt = timestamppb.New(info.ModTime())
		}
		if author := Author(req); author != "" && !strings.EqualFold(author, hit.Author) {
			return fs.SkipDir
		}
		if Matches(hit, req.GetQuery()) {
			hits = append(hits, hit)
		}
		return fs.SkipDir
	})
	if err != nil {
		return nil, err
	}
	SortHits(hits, sort.ID, sort.Ascending)
	return Page(hits, req, c.Limit(req)), nil
}

// Reports whether a directory directly holds at least one file
func locHoldsFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			return true
		}
	}
	return false
}

// The directory of one repo, refusing paths that leave the root
func locDir(c *Client, repo string) (string, error) {
	root, err := locRoot(c)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, filepath.FromSlash(repo))
	if rel, err := filepath.Rel(root, dir); err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("repo %q escapes source root", repo)
	}
	return dir, nil
}

func (localAPI) Resolve(ctx context.Context, c *Client, repo, rev string) (*v1.Model, error) {
	dir, err := locDir(c, repo)
	if err != nil {
		return nil, err
	}
	model := &v1.Model{Repo: repo, Revision: locRevision}
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

func (localAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	dir, err := locDir(c, model.GetRepo())
	if err != nil {
		return nil, err
	}
	return OpenFile(filepath.Join(dir, filepath.FromSlash(artifact.GetPath())))
}
