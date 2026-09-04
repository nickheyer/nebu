package sources

import (
	"context"
	"fmt"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Any git host: trees listed from a partial clone, LFS weights through the batch API; config names each one
var gitrepo = &Catalog{
	ID:            "git",
	Kind:          v1.SourceKind_SOURCE_KIND_GIT,
	Name:          "Git host",
	Transports:    []Use{{Kind: TransportGit, Fields: map[string]string{"endpoint": "", "token_env": "", "username_env": ""}, Required: []string{"endpoint"}}},
	Configured:    true,
	NoBrowse:      true,
	NoSearch:      true,
	Description:   "Any git host such as GitLab, Gitea, or a forge of your own, repositories under one URL with LFS weights",
	RepoExample:   "owner/repo",
	RepoPattern:   `^[\w.-]+(/[\w.-]+)+$`,
	RevisionLabel: "branch or tag",
	Sorts:         []string{SortName},
	API:           gitAPI{},
}

func init() { register(gitrepo) }

// The git transport as a provider: no catalog to search, every repository opens by name
type gitAPI struct{}

// Nothing to list, a git host has no catalog
func (gitAPI) Search(context.Context, *Client, *v1.SearchRequest, Sort) (*v1.SearchResponse, error) {
	return &v1.SearchResponse{}, nil
}

func (gitAPI) Resolve(ctx context.Context, c *Client, repo, revision string) (*v1.Model, error) {
	commit, err := c.Git().Commit(ctx, repo, revision)
	if err != nil {
		return nil, err
	}
	if revision == "" {
		refs, err := c.Git().Refs(ctx, repo)
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			if r.Default {
				revision = r.Name
			}
		}
	}
	artifacts, err := c.Git().List(ctx, repo+"@"+commit)
	if err != nil {
		return nil, err
	}
	return &v1.Model{Repo: repo, Revision: revision, Commit: commit, Artifacts: artifacts}, nil
}

func (gitAPI) Revisions(ctx context.Context, c *Client, repo string) ([]*v1.Revision, error) {
	refs, err := c.Git().Refs(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make([]*v1.Revision, 0, len(refs))
	for _, r := range refs {
		detail := "branch"
		if r.Tag {
			detail = "tag"
		}
		out = append(out, &v1.Revision{Name: r.Name, Commit: r.Commit, Default: r.Default, Detail: detail})
	}
	return out, nil
}

func (gitAPI) Card(ctx context.Context, c *Client, repo, revision string) (*v1.ModelCard, error) {
	page := c.Git().Remote(repo)
	commit, err := c.Git().Commit(ctx, repo, revision)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"README.md", "readme.md", "README"} {
		data, err := c.Git().Read(ctx, repo+"@"+commit+"/"+name, cardMax)
		if err == nil {
			return &v1.ModelCard{Markdown: string(data), Url: page}, nil
		}
	}
	return &v1.ModelCard{Url: page}, nil
}

func (gitAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	ref := model.GetCommit()
	if ref == "" {
		ref = model.GetRevision()
	}
	if ref == "" {
		return nil, fmt.Errorf("%s: no revision to open %s at", model.GetRepo(), artifact.GetPath())
	}
	return c.Git().Open(ctx, model.GetRepo()+"@"+ref+"/"+artifact.GetPath(), int64(artifact.GetSizeBytes()))
}
