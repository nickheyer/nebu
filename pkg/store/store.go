// Package store keeps content addressed blobs, manifests, and a stable link tree.
package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	blobsDir      = "blobs"
	manifestsDir  = "manifests"
	modelsDir     = "models"
	partialSuffix = ".partial"
	stateSuffix   = ".partial.json"
	manifestExt   = ".json"
	digestPrefix  = "sha256:"
	blobPrefix    = "sha256-"
)

// Returned when a model is not in the store
var ErrNotStored = errors.New("model not stored")

// Store rooted at one directory
type Store struct {
	root  string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Opens or creates a store
func Open(root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{blobsDir, manifestsDir, modelsDir} {
		if err := os.MkdirAll(filepath.Join(abs, dir), 0o755); err != nil {
			return nil, err
		}
	}
	return &Store{root: abs, locks: map[string]*sync.Mutex{}}, nil
}

// Returns the store root
func (s *Store) Root() string { return s.root }

// Formats a hex sha256 as a digest
func Digest(hex string) string { return digestPrefix + strings.ToLower(hex) }

// Returns the hex part of a digest
func Hex(digest string) string { return strings.TrimPrefix(digest, digestPrefix) }

// Path of a complete blob
func (s *Store) BlobPath(digest string) string {
	return filepath.Join(s.root, blobsDir, blobPrefix+Hex(digest))
}

// Path of a partial download keyed by name
func (s *Store) PartialPath(key string) string {
	return filepath.Join(s.root, blobsDir, key+partialSuffix)
}

// Reports whether a complete blob exists
func (s *Store) HasBlob(digest string) bool {
	info, err := os.Stat(s.BlobPath(digest))
	return err == nil && info.Mode().IsRegular()
}

// Moves a verified partial into place
func (s *Store) Commit(partial, digest string) error {
	dest := s.BlobPath(digest)
	if s.HasBlob(digest) {
		return os.Remove(partial)
	}
	return os.Rename(partial, dest)
}

// Adopts a local file by hard link, falling back to copy
func (s *Store) Adopt(path, digest string) error {
	if s.HasBlob(digest) {
		return nil
	}
	dest := s.BlobPath(digest)
	if err := os.Link(path, dest); err == nil {
		return nil
	}
	tmp := dest + partialSuffix
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(tmp)
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// Removes a blob, ignoring absence
func (s *Store) RemoveBlob(digest string) error {
	err := os.Remove(s.BlobPath(digest))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Serializes work on a key and returns the unlock
func (s *Store) Lock(key string) func() {
	s.mu.Lock()
	m, ok := s.locks[key]
	if !ok {
		m = &sync.Mutex{}
		s.locks[key] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// Stable key for a stored model
func Key(source, repo, group string) string {
	return strings.Join([]string{source, repo, group}, "/")
}

func (s *Store) manifestPath(source, repo, group string) (string, error) {
	rel, err := safeJoin(source, repo, group+manifestExt)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.root, manifestsDir, rel), nil
}

// Directory holding the links of one stored model
func (s *Store) GroupDir(source, repo, group string) (string, error) {
	rel, err := safeJoin(source, repo, group)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.root, modelsDir, rel), nil
}

// Writes a manifest atomically
func (s *Store) WriteManifest(m *v1.StoredModel) error {
	path, err := s.manifestPath(m.GetSourceId(), m.GetRepo(), m.GetGroup())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(m)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Reads one manifest
func (s *Store) ReadManifest(source, repo, group string) (*v1.StoredModel, error) {
	path, err := s.manifestPath(source, repo, group)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotStored, Key(source, repo, group))
	}
	if err != nil {
		return nil, err
	}
	m := &v1.StoredModel{}
	if err := protojson.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// Lists every manifest sorted by key
func (s *Store) ListManifests() ([]*v1.StoredModel, error) {
	var out []*v1.StoredModel
	root := filepath.Join(s.root, manifestsDir)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, manifestExt) {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		m := &v1.StoredModel{}
		if err := protojson.Unmarshal(data, m); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return Key(out[i].GetSourceId(), out[i].GetRepo(), out[i].GetGroup()) < Key(out[j].GetSourceId(), out[j].GetRepo(), out[j].GetGroup())
	})
	return out, nil
}

// Removes a manifest and its link directory
func (s *Store) RemoveManifest(source, repo, group string) (*v1.StoredModel, error) {
	m, err := s.ReadManifest(source, repo, group)
	if err != nil {
		return nil, err
	}
	path, _ := s.manifestPath(source, repo, group)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	dir, _ := s.GroupDir(source, repo, group)
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	s.pruneEmpty(filepath.Dir(path), filepath.Join(s.root, manifestsDir))
	s.pruneEmpty(filepath.Dir(dir), filepath.Join(s.root, modelsDir))
	return m, nil
}

// Creates a relative symlink to a blob and returns the link path
func (s *Store) Link(source, repo, group, rel, digest string) (string, error) {
	dir, err := s.GroupDir(source, repo, group)
	if err != nil {
		return "", err
	}
	clean, err := safeJoin(rel)
	if err != nil {
		return "", err
	}
	link := filepath.Join(dir, clean)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return "", err
	}
	target, err := filepath.Rel(filepath.Dir(link), s.BlobPath(digest))
	if err != nil {
		return "", err
	}
	if existing, err := os.Readlink(link); err == nil && existing == target {
		return link, nil
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Symlink(target, link); err != nil {
		return "", err
	}
	return link, nil
}

// Removes one link, ignoring absence
func (s *Store) Unlink(link string) error {
	err := os.Remove(link)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Removes blobs no manifest references
func (s *Store) Gc(partials bool) (*v1.GcResponse, error) {
	manifests, err := s.ListManifests()
	if err != nil {
		return nil, err
	}
	referenced := map[string]bool{}
	for _, m := range manifests {
		for _, a := range m.GetArtifacts() {
			referenced[filepath.Base(s.BlobPath(a.GetDigest()))] = true
		}
	}
	entries, err := os.ReadDir(filepath.Join(s.root, blobsDir))
	if err != nil {
		return nil, err
	}
	resp := &v1.GcResponse{}
	for _, e := range entries {
		name := e.Name()
		partial := strings.HasSuffix(name, partialSuffix) || strings.HasSuffix(name, stateSuffix)
		if partial && !partials {
			continue
		}
		if !partial && (referenced[name] || !strings.HasPrefix(name, blobPrefix)) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if err := os.Remove(filepath.Join(s.root, blobsDir, name)); err != nil {
			return nil, err
		}
		resp.Removed++
		resp.FreedBytes += uint64(info.Size())
	}
	return resp, nil
}

// Counts models, blobs, and partials
func (s *Store) Status() (*v1.StoreStatus, error) {
	manifests, err := s.ListManifests()
	if err != nil {
		return nil, err
	}
	st := &v1.StoreStatus{Path: s.root, Models: uint64(len(manifests))}
	entries, err := os.ReadDir(filepath.Join(s.root, blobsDir))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		switch {
		case strings.HasSuffix(e.Name(), partialSuffix):
			st.Partials++
			st.PartialBytes += uint64(info.Size())
		case strings.HasPrefix(e.Name(), blobPrefix):
			st.Blobs++
			st.BlobBytes += uint64(info.Size())
		}
	}
	return st, nil
}

func (s *Store) pruneEmpty(dir, stop string) {
	for dir != stop && strings.HasPrefix(dir, stop) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// Joins path segments, rejecting traversal
func safeJoin(segments ...string) (string, error) {
	var parts []string
	for _, seg := range segments {
		for _, piece := range strings.Split(filepath.ToSlash(seg), "/") {
			if piece == "" || piece == "." {
				continue
			}
			if piece == ".." {
				return "", fmt.Errorf("path %q escapes the store", seg)
			}
			parts = append(parts, piece)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("empty path")
	}
	return filepath.Join(parts...), nil
}
