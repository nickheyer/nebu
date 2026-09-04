package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	index "github.com/nickheyer/nebu/pkg/mirror"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A store another nebu exported, read over HTTP, S3, or a directory; config names each one
var mirror = &Catalog{
	ID:   "mirror",
	Kind: v1.SourceKind_SOURCE_KIND_MIRROR,
	Name: "Mirror",
	Transports: []Use{
		{Kind: TransportHTTP, Fields: map[string]string{"endpoint": "", "token_env": ""}},
		{Kind: TransportFile, Fields: map[string]string{"path": ""}},
	},
	Configured:    true,
	Description:   "A store another nebu exported, read over HTTP, S3, or a directory",
	RepoExample:   "org/model",
	RepoPattern:   `^[\w.-]+/[\w.-]+$`,
	RevisionLabel: "revision",
	Sorts:         []string{SortName, SortUpdated},
	Reversible:    []string{SortName, SortUpdated},
	DefaultLimit:  50,
	MaxLimit:      500,
	API:           mirrorAPI{},
}

func init() { register(mirror) }

const mirRevision = "mirror"

// An exported store: index files where the export wrote them, else an object listing or a directory walk
type mirrorAPI struct{}

// Needs a directory or an endpoint, the directory wins when both are set
func (mirrorAPI) Check(c *Client) error {
	if c.File() == nil && c.HTTP() == nil {
		return fmt.Errorf("a mirror needs endpoint or path")
	}
	return nil
}

// The transport a mirror reads through, the directory when it has one
func mirTransport(c *Client) Transport {
	if f := c.File(); f != nil {
		return f
	}
	return c.HTTP()
}

func (mirrorAPI) Search(ctx context.Context, c *Client, req *v1.SearchRequest, sort Sort) (*v1.SearchResponse, error) {
	var root index.Root
	if err := mirReadJSON(ctx, c, index.IndexFile, &root); err != nil {
		repos, lerr := mirListPrefixes(ctx, c)
		if lerr != nil {
			return nil, err
		}
		for _, r := range repos {
			root.Repos = append(root.Repos, index.RepoEntry{Repo: r})
		}
	}
	var hits []*v1.SearchHit
	for _, r := range root.Repos {
		author, name, _ := strings.Cut(r.Repo, "/")
		hit := &v1.SearchHit{Repo: r.Repo, Name: name, Author: author}
		if name == "" {
			hit.Name, hit.Author = r.Repo, ""
		}
		if !r.UpdatedAt.IsZero() {
			hit.UpdatedAt = timestamppb.New(r.UpdatedAt)
		}
		if r.Files > 0 {
			hit.Extra = map[string]string{"files": strconv.Itoa(r.Files)}
		}
		if author := Author(req); author != "" && !strings.EqualFold(author, hit.Author) {
			continue
		}
		if Matches(hit, req.GetQuery()) {
			hits = append(hits, hit)
		}
	}
	SortHits(hits, sort.ID, sort.Ascending)
	return Page(hits, req, c.Limit(req)), nil
}

func (mirrorAPI) Resolve(ctx context.Context, c *Client, repo, rev string) (*v1.Model, error) {
	if strings.Contains(repo, "..") {
		return nil, fmt.Errorf("repo %q escapes the mirror", repo)
	}
	model := &v1.Model{Repo: repo, Revision: mirRevision}
	var idx index.Index
	if err := mirReadJSON(ctx, c, path.Join(repo, index.IndexFile), &idx); err == nil {
		model.Commit = idx.Commit
		if idx.Revision != "" {
			model.Revision = idx.Revision
		}
		for _, f := range idx.Files {
			model.Artifacts = append(model.Artifacts, &v1.Artifact{Path: f.Path, SizeBytes: f.Size, Sha256: strings.ToLower(f.Sha256)})
		}
		return model, nil
	}
	files, err := mirTransport(c).List(ctx, repo)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if path.Base(f.GetPath()) != index.IndexFile {
			model.Artifacts = append(model.Artifacts, f)
		}
	}
	if len(model.Artifacts) == 0 {
		return nil, fmt.Errorf("%s: no index and no objects in the mirror", repo)
	}
	return model, nil
}

func (mirrorAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	return mirTransport(c).Open(ctx, path.Join(model.GetRepo(), artifact.GetPath()), int64(artifact.GetSizeBytes()))
}

func mirReadJSON(ctx context.Context, c *Client, rel string, out any) error {
	data, err := mirTransport(c).Read(ctx, rel, cardMax)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// Lists repositories as two level prefixes, or directories
func mirListPrefixes(ctx context.Context, c *Client) ([]string, error) {
	var out []string
	if f := c.File(); f != nil {
		owners, err := f.Dirs("")
		if err != nil {
			return nil, err
		}
		for _, o := range owners {
			names, err := f.Dirs(o)
			if err != nil {
				continue
			}
			for _, n := range names {
				out = append(out, o+"/"+n)
			}
		}
		return out, nil
	}
	owners, err := c.HTTP().Prefixes(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, o := range owners {
		names, err := c.HTTP().Prefixes(ctx, o)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			out = append(out, o+"/"+n)
		}
	}
	return out, nil
}
