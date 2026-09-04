package sources

import (
	"context"
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
	Name:          "Local directory",
	Transports:    []Use{{Kind: TransportFile, Fields: map[string]string{"path": ""}, Required: []string{"path"}}},
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

const locRevision = "local"

// A directory of model directories: owner/name two levels deep, files read in place
type localAPI struct{}

func (localAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	root := c.File()
	var hits []*v1.SearchHit
	add := func(repo string) error {
		dir, err := root.Path(repo)
		if err != nil {
			return err
		}
		author, name, _ := strings.Cut(repo, "/")
		hit := &v1.SearchHit{Repo: repo, Name: name, Author: author}
		if name == "" {
			hit.Name, hit.Author = repo, ""
		}
		if info, err := os.Stat(dir); err == nil {
			hit.UpdatedAt = timestamppb.New(info.ModTime())
		}
		if author := Author(req); author != "" && !strings.EqualFold(author, hit.Author) {
			return nil
		}
		if Matches(hit, req.GetQuery()) {
			hits = append(hits, hit)
		}
		return nil
	}
	owners, err := root.Dirs("")
	if err != nil {
		return nil, err
	}
	for _, owner := range owners {
		// A directory holding files is a repo, a directory of only directories is an owner
		if locHoldsFiles(filepath.Join(root.Root(), owner)) {
			if err := add(owner); err != nil {
				return nil, err
			}
			continue
		}
		names, err := root.Dirs(owner)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if err := add(owner + "/" + name); err != nil {
				return nil, err
			}
		}
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

func (localAPI) Resolve(ctx context.Context, c *Client, repo, rev string) (*v1.Model, error) {
	artifacts, err := c.File().List(ctx, repo)
	if err != nil {
		return nil, err
	}
	return &v1.Model{Repo: repo, Revision: locRevision, Artifacts: artifacts}, nil
}

func (localAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	return c.File().Open(ctx, model.GetRepo()+"/"+artifact.GetPath(), int64(artifact.GetSizeBytes()))
}
