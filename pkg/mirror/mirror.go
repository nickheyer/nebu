// Package mirror defines index files exports write and mirrors read.
package mirror

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Index file name at the root and in every repo
const IndexFile = "index.json"

// One artifact in a repo index
type File struct {
	Path   string `json:"path"`
	Size   uint64 `json:"size"`
	Sha256 string `json:"sha256,omitempty"`
}

// Index of one repository directory
type Index struct {
	Repo      string    `json:"repo"`
	Revision  string    `json:"revision,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	Files     []File    `json:"files"`
}

// Adds or replaces a file entry by path
func (i *Index) Put(f File) {
	for n, existing := range i.Files {
		if existing.Path == f.Path {
			i.Files[n] = f
			return
		}
	}
	i.Files = append(i.Files, f)
	sort.Slice(i.Files, func(a, b int) bool { return i.Files[a].Path < i.Files[b].Path })
}

// One repository in the root index
type RepoEntry struct {
	Repo      string    `json:"repo"`
	Commit    string    `json:"commit,omitempty"`
	Files     int       `json:"files"`
	UpdatedAt time.Time `json:"updated_at"`
	// Weight formats the files hold, so a mirror lists by format without reading every index.
	Formats []string `json:"formats,omitempty"`
}

// Root index listing every repository
type Root struct {
	Repos []RepoEntry `json:"repos"`
}

// Reads a repo index
func ReadIndex(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var index Index
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	return &index, nil
}

// Writes a repo index atomically
func WriteIndex(path string, index *Index) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
