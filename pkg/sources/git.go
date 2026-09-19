package sources

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	// Blobs up to this size arrive with the clone, so configs and LFS pointers are local
	gitBlobLimit = "1m"
	// A blob this small may be an LFS pointer
	gitPointerMax = 1024
	gitLFSSpec    = "version https://git-lfs.github.com/spec/v1"
	gitLFSMedia   = "application/vnd.git-lfs+json"
	gitLFSGrace   = 30 * time.Second
	gitLFSTTL     = 10 * time.Minute
)

var (
	commitRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	lfsOidRe = regexp.MustCompile(`(?m)^oid sha256:([0-9a-f]{64})$`)
	lfsSzRe  = regexp.MustCompile(`(?m)^size (\d+)$`)
)

// One ref a remote advertises
type GitRef struct {
	Name    string
	Commit  string
	Tag     bool
	Default bool
}

type gitEntry struct {
	oid  string
	size int64
	lfs  bool
	// Present blobs are in the clone, the rest fetch on demand
	present bool
	sha256  string
}

// Git transport with cached partial clones, on-demand blobs, and LFS batch downloads. Locators use
// repo@ref or repo@ref/path, where repo is an endpoint path or full URL.
type Git struct {
	endpoint string
	username string
	token    string
	dir      string
	plain    *HTTP
	mu       sync.Mutex
	locks    map[string]*sync.Mutex
	trees    map[string]map[string]*gitEntry
	commits  map[string]string
}

func newGitTransport(cfg map[string]string, env transportEnv) (Transport, error) {
	g := &Git{
		endpoint: cfg["endpoint"],
		username: envValue(cfg, "username_env"),
		token:    envValue(cfg, "token_env"),
		dir:      filepath.Join(env.cacheDir, TransportGit),
		locks:    map[string]*sync.Mutex{},
		trees:    map[string]map[string]*gitEntry{},
		commits:  map[string]string{},
	}
	if g.username == "" {
		g.username = "git"
	}
	var err error
	if g.plain, err = NewHTTP("https://localhost", ""); err != nil {
		return nil, err
	}
	return g, nil
}

// The clone URL of a repo, joined onto the endpoint unless it is one already
func (g *Git) Remote(repo string) string {
	if strings.Contains(repo, "://") || strings.HasPrefix(repo, "/") || strings.HasPrefix(repo, ".") || strings.HasPrefix(repo, "git@") {
		return repo
	}
	return strings.TrimRight(g.endpoint, "/") + "/" + strings.Trim(repo, "/")
}

func (g *Git) cloneDir(remote string) string {
	sum := sha256.Sum256([]byte(remote))
	return filepath.Join(g.dir, hex.EncodeToString(sum[:8])+".git")
}

func (g *Git) lock(key string) func() {
	g.mu.Lock()
	m, ok := g.locks[key]
	if !ok {
		m = &sync.Mutex{}
		g.locks[key] = m
	}
	g.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// The basic auth header value for the token, empty without one
func (g *Git) basic() string {
	if g.token == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(g.username+":"+g.token))
}

// Runs git with prompts off, LFS smudging off, and the token as basic auth
func (g *Git) git(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git is not installed on this host")
	}
	full := []string{}
	if auth := g.basic(); auth != "" {
		full = append(full, "-c", "http.extraHeader=Authorization: "+auth)
	}
	if dir != "" {
		full = append(full, "-C", dir)
	}
	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1")
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], msg)
	}
	return stdout.Bytes(), nil
}

// Lists the branches and tags a repo advertises, the default branch marked
func (g *Git) Refs(ctx context.Context, repo string) ([]GitRef, error) {
	out, err := g.git(ctx, "", nil, "ls-remote", "--symref", g.Remote(repo))
	if err != nil {
		return nil, err
	}
	head := ""
	peeled := map[string]string{}
	var refs []GitRef
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		left, right, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(left, "ref: ") && right == "HEAD":
			head = strings.TrimPrefix(left, "ref: ")
		case strings.HasPrefix(right, "refs/heads/"):
			refs = append(refs, GitRef{Name: strings.TrimPrefix(right, "refs/heads/"), Commit: left})
		case strings.HasPrefix(right, "refs/tags/"):
			name := strings.TrimPrefix(right, "refs/tags/")
			if strings.HasSuffix(name, "^{}") {
				peeled[strings.TrimSuffix(name, "^{}")] = left
				continue
			}
			refs = append(refs, GitRef{Name: name, Commit: left, Tag: true})
		}
	}
	for i := range refs {
		if refs[i].Tag {
			if c, ok := peeled[refs[i].Name]; ok {
				refs[i].Commit = c
			}
		}
		refs[i].Default = !refs[i].Tag && "refs/heads/"+refs[i].Name == head
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].Default != refs[j].Default {
			return refs[i].Default
		}
		return refs[i].Tag != refs[j].Tag && !refs[i].Tag
	})
	return refs, nil
}

// Makes sure the commit a ref names is in the clone, returning it
func (g *Git) ensure(ctx context.Context, repo, ref string) (string, error) {
	remote := g.Remote(repo)
	dir := g.cloneDir(remote)
	unlock := g.lock(dir)
	defer unlock()
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		for _, args := range [][]string{
			{"init", "--bare", "--quiet", dir},
			{"-C", dir, "remote", "add", "origin", remote},
			{"-C", dir, "config", "remote.origin.promisor", "true"},
			{"-C", dir, "config", "remote.origin.partialclonefilter", "blob:limit=" + gitBlobLimit},
			{"-C", dir, "config", "extensions.partialClone", "origin"},
		} {
			if _, err := g.git(ctx, "", nil, args...); err != nil {
				os.RemoveAll(dir)
				return "", err
			}
		}
	}
	want := ref
	if ref == "" {
		want = "HEAD"
	}
	commit := ""
	if commitRe.MatchString(want) {
		commit = want
	} else {
		refs, err := g.Refs(ctx, repo)
		if err != nil {
			return "", err
		}
		for _, r := range refs {
			if r.Name == want || (want == "HEAD" && r.Default) {
				commit = r.Commit
				break
			}
		}
		if commit == "" {
			return "", fmt.Errorf("%s: no branch or tag %q", repo, want)
		}
	}
	// Check fetched refs without inspecting objects, which could trigger an incomplete lazy fetch.
	marker := "refs/nebu/fetched/" + commit
	if _, err := g.git(ctx, dir, nil, "show-ref", "--verify", "--quiet", marker); err == nil {
		return commit, nil
	}
	if _, err := g.git(ctx, dir, nil, "fetch", "--quiet", "--depth=1", "--no-tags", "--filter=blob:limit="+gitBlobLimit, "origin", commit); err != nil {
		// Hosts that refuse fetching by hash still serve the ref by name
		if _, rerr := g.git(ctx, dir, nil, "fetch", "--quiet", "--depth=1", "--no-tags", "--filter=blob:limit="+gitBlobLimit, "origin", want); rerr != nil {
			return "", err
		}
	}
	head, err := g.git(ctx, dir, nil, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return "", err
	}
	if got := strings.TrimSpace(string(head)); got != commit {
		if !commitRe.MatchString(want) || !strings.HasPrefix(got, want) {
			return "", fmt.Errorf("%s: fetched %s for %s, wanted %s", repo, got, want, commit)
		}
		commit = got
	}
	if _, err := g.git(ctx, dir, nil, "update-ref", marker, commit); err != nil {
		return "", err
	}
	return commit, nil
}

// Resolves a ref to the commit it names, fetching it into the clone
func (g *Git) Commit(ctx context.Context, repo, ref string) (string, error) {
	return g.ensure(ctx, repo, ref)
}

// Reads the tree of a commit, sizes of local blobs, and LFS pointers, once per commit
func (g *Git) entries(ctx context.Context, repo, ref string) (string, map[string]*gitEntry, error) {
	commit, err := g.ensure(ctx, repo, ref)
	if err != nil {
		return "", nil, err
	}
	remote := g.Remote(repo)
	key := remote + "\x00" + commit
	g.mu.Lock()
	tree, ok := g.trees[key]
	g.mu.Unlock()
	if ok {
		return commit, tree, nil
	}
	dir := g.cloneDir(remote)
	out, err := g.git(ctx, dir, nil, "ls-tree", "-r", "-z", commit)
	if err != nil {
		return "", nil, err
	}
	tree = map[string]*gitEntry{}
	var oids []string
	for _, rec := range bytes.Split(out, []byte{0}) {
		meta, p, ok := strings.Cut(string(rec), "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[1] != "blob" || fields[0] == "120000" {
			continue
		}
		tree[p] = &gitEntry{oid: fields[2]}
		oids = append(oids, fields[2])
	}
	missing := map[string]bool{}
	if out, err = g.git(ctx, dir, nil, "rev-list", "--objects", "--missing=print", "--no-object-names", commit); err != nil {
		return "", nil, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "?") {
			missing[strings.TrimSpace(line[1:])] = true
		}
	}
	var present []string
	for _, oid := range oids {
		if !missing[oid] {
			present = append(present, oid)
		}
	}
	sizes := map[string]int64{}
	if len(present) > 0 {
		out, err = g.git(ctx, dir, strings.NewReader(strings.Join(present, "\n")+"\n"), "cat-file", "--batch-check")
		if err != nil {
			return "", nil, err
		}
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 3 && fields[1] == "blob" {
				sizes[fields[0]], _ = strconv.ParseInt(fields[2], 10, 64)
			}
		}
	}
	var small []string
	for _, e := range tree {
		if size, ok := sizes[e.oid]; ok {
			e.size, e.present = size, true
			if size <= gitPointerMax {
				small = append(small, e.oid)
			}
		}
	}
	pointers, err := g.pointers(ctx, dir, small)
	if err != nil {
		return "", nil, err
	}
	for _, e := range tree {
		if p, ok := pointers[e.oid]; ok {
			e.lfs, e.sha256, e.size = true, p.sha256, p.size
		}
	}
	g.mu.Lock()
	g.trees[key] = tree
	g.mu.Unlock()
	return commit, tree, nil
}

type lfsPointer struct {
	sha256 string
	size   int64
}

// Reads small blobs in one batch and keeps the ones that are LFS pointers
func (g *Git) pointers(ctx context.Context, dir string, oids []string) (map[string]lfsPointer, error) {
	out := map[string]lfsPointer{}
	if len(oids) == 0 {
		return out, nil
	}
	sort.Strings(oids)
	data, err := g.git(ctx, dir, strings.NewReader(strings.Join(oids, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	r := bufio.NewReader(bytes.NewReader(data))
	for {
		header, err := r.ReadString('\n')
		if err != nil {
			break
		}
		fields := strings.Fields(header)
		if len(fields) != 3 {
			continue
		}
		size, _ := strconv.Atoi(fields[2])
		body := make([]byte, size+1)
		if _, err := io.ReadFull(r, body); err != nil {
			break
		}
		content := string(body[:size])
		if !strings.HasPrefix(content, gitLFSSpec) {
			continue
		}
		oid := lfsOidRe.FindStringSubmatch(content)
		sz := lfsSzRe.FindStringSubmatch(content)
		if oid == nil || sz == nil {
			continue
		}
		n, _ := strconv.ParseInt(sz[1], 10, 64)
		out[fields[0]] = lfsPointer{sha256: oid[1], size: n}
	}
	return out, nil
}

// Lists the files of a commit, LFS files carrying their real size and digest
func (g *Git) List(ctx context.Context, locator string) ([]*v1.Artifact, error) {
	repo, ref, rest := splitRepoRef(locator)
	if rest != "" {
		return nil, fmt.Errorf("locator %q: want repo@ref", locator)
	}
	_, tree, err := g.entries(ctx, repo, ref)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(tree))
	for p := range tree {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]*v1.Artifact, 0, len(paths))
	for _, p := range paths {
		e := tree[p]
		out = append(out, &v1.Artifact{Path: p, SizeBytes: uint64(e.size), Sha256: e.sha256})
	}
	return out, nil
}

// Opens one file: an LFS object through the batch API, a plain blob extracted from the clone
func (g *Git) Open(ctx context.Context, locator string, size int64) (Blob, error) {
	repo, ref, rel := splitRepoRef(locator)
	if rel == "" {
		return nil, fmt.Errorf("locator %q: want repo@ref/path", locator)
	}
	_, tree, err := g.entries(ctx, repo, ref)
	if err != nil {
		return nil, err
	}
	e, ok := tree[rel]
	if !ok {
		return nil, fmt.Errorf("%s: no file %s at %s", repo, rel, ref)
	}
	if e.lfs {
		return g.lfsBlob(repo, e), nil
	}
	// Delay extraction until the transfer schedule permits it.
	return &lazyBlob{size: e.size, whole: true, land: func(ctx context.Context, _ func(int64)) (Blob, error) {
		return g.extract(ctx, repo, e)
	}}, nil
}

// Reads one file whole, capped
func (g *Git) Read(ctx context.Context, locator string, max int64) ([]byte, error) {
	b, err := g.Open(ctx, locator, 0)
	if err != nil {
		return nil, err
	}
	defer b.Close()
	rc, err := RangeOf(ctx, b, 0, b.Size())
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return readAllCapped(rc, max)
}

// Writes a plain blob out of the clone into scratch
func (g *Git) extract(ctx context.Context, repo string, e *gitEntry) (Blob, error) {
	dir := g.cloneDir(g.Remote(repo))
	scratch := filepath.Join(g.dir, "blobs")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return nil, err
	}
	dest := filepath.Join(scratch, e.oid)
	unlock := g.lock(dest)
	defer unlock()
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git is not installed on this host")
	}
	f, err := os.Create(dest + ".partial")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "blob", e.oid)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if auth := g.basic(); auth != "" {
		cmd.Args = append([]string{cmd.Args[0], "-c", "http.extraHeader=Authorization: " + auth}, cmd.Args[1:]...)
	}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = f, &stderr
	err = cmd.Run()
	f.Close()
	if err != nil {
		os.Remove(dest + ".partial")
		return nil, fmt.Errorf("git cat-file %s: %s", e.oid, strings.TrimSpace(stderr.String()))
	}
	if err := os.Rename(dest+".partial", dest); err != nil {
		return nil, err
	}
	return openFile(dest, true)
}

type lfsBatch struct {
	Objects []struct {
		Oid     string `json:"oid"`
		Size    int64  `json:"size"`
		Actions map[string]struct {
			Href      string            `json:"href"`
			Header    map[string]string `json:"header"`
			ExpiresIn int               `json:"expires_in"`
			ExpiresAt string            `json:"expires_at"`
		} `json:"actions"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"objects"`
	Message string `json:"message"`
}

// Returns the LFS batch endpoint, translating scp remotes to HTTPS.
func (g *Git) lfsURL(repo string) string {
	remote := g.Remote(repo)
	if _, rest, ok := strings.Cut(remote, "@"); ok && !strings.Contains(remote, "://") {
		remote = "https://" + strings.Replace(rest, ":", "/", 1)
	}
	return strings.TrimSuffix(remote, ".git") + ".git/info/lfs/objects/batch"
}

// Opens an LFS object as a range blob whose download link is fetched from the batch API and refreshed as it expires
func (g *Git) lfsBlob(repo string, e *gitEntry) Blob {
	var mu sync.Mutex
	var href string
	var header http.Header
	var until time.Time
	resolve := func(ctx context.Context) (string, http.Header, error) {
		mu.Lock()
		defer mu.Unlock()
		if href != "" && time.Now().Before(until) {
			return href, header, nil
		}
		body := map[string]any{"operation": "download", "transfers": []string{"basic"}, "objects": []map[string]any{{"oid": e.sha256, "size": e.size}}}
		h := http.Header{"Accept": {gitLFSMedia}}
		if auth := g.basic(); auth != "" {
			h.Set("Authorization", auth)
		}
		var resp lfsBatch
		data, err := json.Marshal(body)
		if err != nil {
			return "", nil, err
		}
		r, err := g.plain.DoBody(ctx, http.MethodPost, g.lfsURL(repo), nil, mergeHeader(h, http.Header{"Content-Type": {gitLFSMedia}}), data)
		if err != nil {
			return "", nil, fmt.Errorf("lfs batch: %w", err)
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
			return "", nil, fmt.Errorf("lfs batch: %w", err)
		}
		if len(resp.Objects) == 0 {
			return "", nil, fmt.Errorf("lfs batch: no object in the answer: %s", resp.Message)
		}
		obj := resp.Objects[0]
		if obj.Error != nil {
			return "", nil, fmt.Errorf("lfs %s: %s", e.sha256[:12], obj.Error.Message)
		}
		action, ok := obj.Actions["download"]
		if !ok || action.Href == "" {
			return "", nil, fmt.Errorf("lfs %s: no download action", e.sha256[:12])
		}
		href = action.Href
		header = http.Header{}
		for k, v := range action.Header {
			header.Set(k, v)
		}
		ttl := gitLFSTTL
		if action.ExpiresIn > 0 {
			ttl = time.Duration(action.ExpiresIn) * time.Second
		} else if at, err := time.Parse(time.RFC3339, action.ExpiresAt); err == nil {
			ttl = time.Until(at)
		}
		until = time.Now().Add(ttl - gitLFSGrace)
		return href, header, nil
	}
	return NewRangeBlob(g.plain, "", e.size).WithResolver(resolve)
}

func mergeHeader(base, extra http.Header) http.Header {
	out := http.Header{}
	for k, vs := range base {
		out[k] = vs
	}
	for k, vs := range extra {
		out[k] = vs
	}
	return out
}

// Clones ref into dest and returns the commit. Empty or latest selects the default branch. Commit
// hashes use full clones, while branches and tags use shallow clones.
func Checkout(ctx context.Context, remote, ref, dest string, out io.Writer) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("git is not installed on this host")
	}
	os.RemoveAll(dest)
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Stdout, cmd.Stderr = out, out
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		return cmd.Run()
	}
	clone := func(args ...string) error {
		return run(append([]string{"clone"}, args...)...)
	}
	fmt.Fprintf(out, "cloning %s at %s\n", remote, ref)
	switch {
	case ref == "" || ref == "latest":
		if err := clone("--depth", "1", remote, dest); err != nil {
			return "", err
		}
	case commitRe.MatchString(ref):
		if err := clone(remote, dest); err != nil {
			return "", err
		}
		if err := run("-C", dest, "checkout", "--quiet", ref); err != nil {
			return "", err
		}
	default:
		if err := clone("--depth", "1", "--branch", ref, remote, dest); err != nil {
			return "", err
		}
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dest, "rev-parse", "HEAD")
	data, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Any git host: trees from a partial clone, LFS weights through the batch API
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
		out = append(out, refRevision(r.Name, r.Commit, r.Default, r.Tag))
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
