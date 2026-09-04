package sources

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
	"strconv"
	"strings"

	index "github.com/nickheyer/nebu/pkg/mirror"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A store another nebu exported, read over HTTP, S3, or a directory; config names each one
var mirror = &Catalog{
	ID:            "mirror",
	Kind:          v1.SourceKind_SOURCE_KIND_MIRROR,
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

// Needs a directory or an endpoint from config
func (mirrorAPI) Check(c *Client) error {
	if c.Spec().GetPath() == "" && c.Spec().GetEndpoint() == "" {
		return fmt.Errorf("mirror source needs endpoint or path")
	}
	_, err := mirDir(c)
	return err
}

// The directory a mirror reads from, empty when it reads over HTTP
func mirDir(c *Client) (string, error) {
	if c.Spec().GetPath() == "" {
		return "", nil
	}
	return filepath.Abs(c.Spec().GetPath())
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
	files, err := mirListObjects(ctx, c, repo)
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

func (mirrorAPI) Open(ctx context.Context, c *Client, model *v1.Model, artifact *v1.Artifact) (Blob, error) {
	rel := path.Join(model.GetRepo(), artifact.GetPath())
	dir, err := mirDir(c)
	if err != nil {
		return nil, err
	}
	if dir != "" {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if r, err := filepath.Rel(dir, full); err != nil || strings.HasPrefix(r, "..") {
			return nil, fmt.Errorf("%s escapes the mirror", rel)
		}
		return OpenFile(full)
	}
	return c.Range(c.URL(rel), artifact)
}

func mirReadJSON(ctx context.Context, c *Client, rel string, out any) error {
	dir, err := mirDir(c)
	if err != nil {
		return err
	}
	if dir != "" {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		return json.Unmarshal(data, out)
	}
	_, err = c.JSON(ctx, c.URL(rel), nil, out)
	return err
}

// One object from an S3 style listing
type mirObject struct {
	Path string
	Size uint64
}

type mirListResult struct {
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
func mirListObjects(ctx context.Context, c *Client, repo string) ([]mirObject, error) {
	dir, err := mirDir(c)
	if err != nil {
		return nil, err
	}
	if dir != "" {
		var out []mirObject
		root := filepath.Join(dir, filepath.FromSlash(repo))
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() == index.IndexFile {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			out = append(out, mirObject{Path: filepath.ToSlash(rel), Size: uint64(info.Size())})
			return nil
		})
		return out, err
	}
	var out []mirObject
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {repo + "/"}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		var res mirListResult
		if err := mirReadXML(ctx, c, q, &res); err != nil {
			return nil, err
		}
		for _, o := range res.Contents {
			rel := strings.TrimPrefix(o.Key, repo+"/")
			if rel == "" || strings.HasSuffix(rel, "/") || path.Base(rel) == index.IndexFile {
				continue
			}
			out = append(out, mirObject{Path: rel, Size: o.Size})
		}
		if !res.IsTruncated || res.NextContinuationToken == "" {
			return out, nil
		}
		token = res.NextContinuationToken
	}
}

// Lists repositories as two level prefixes, or directories
func mirListPrefixes(ctx context.Context, c *Client) ([]string, error) {
	dir, err := mirDir(c)
	if err != nil {
		return nil, err
	}
	if dir != "" {
		var out []string
		owners, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, o := range owners {
			if !o.IsDir() {
				continue
			}
			names, err := os.ReadDir(filepath.Join(dir, o.Name()))
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
	var owners mirListResult
	if err := mirReadXML(ctx, c, url.Values{"list-type": {"2"}, "delimiter": {"/"}}, &owners); err != nil {
		return nil, err
	}
	var out []string
	for _, o := range owners.CommonPrefixes {
		var names mirListResult
		if err := mirReadXML(ctx, c, url.Values{"list-type": {"2"}, "delimiter": {"/"}, "prefix": {o.Prefix}}, &names); err != nil {
			return nil, err
		}
		for _, n := range names.CommonPrefixes {
			out = append(out, strings.TrimSuffix(n.Prefix, "/"))
		}
	}
	return out, nil
}

func mirReadXML(ctx context.Context, c *Client, q url.Values, out any) error {
	resp, err := c.Do(ctx, http.MethodGet, c.Base()+"/", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return xml.NewDecoder(resp.Body).Decode(out)
}
