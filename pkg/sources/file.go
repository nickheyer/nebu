package sources

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Filesystem transport rooted at one directory, files read in place
type File struct {
	root string
}

func newFileTransport(cfg map[string]string, _ transportEnv) (Transport, error) {
	if cfg["path"] == "" {
		return nil, nil
	}
	root, err := filepath.Abs(cfg["path"])
	if err != nil {
		return nil, err
	}
	return &File{root: root}, nil
}

// The directory files are read under
func (f *File) Root() string { return f.root }

// Resolves a locator under the root, refusing paths that leave it
func (f *File) Path(locator string) (string, error) {
	full := filepath.Join(f.root, filepath.FromSlash(locator))
	if rel, err := filepath.Rel(f.root, full); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q escapes %s", locator, f.root)
	}
	return full, nil
}

// Lists every file under a directory with its size
func (f *File) List(ctx context.Context, locator string) ([]*v1.Artifact, error) {
	dir, err := f.Path(locator)
	if err != nil {
		return nil, err
	}
	var out []*v1.Artifact
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return ctx.Err()
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, &v1.Artifact{Path: filepath.ToSlash(rel), SizeBytes: uint64(info.Size())})
		return nil
	})
	return out, err
}

// Lists the directories directly under a directory
func (f *File) Dirs(locator string) ([]string, error) {
	dir, err := f.Path(locator)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Opens a file in place
func (f *File) Open(ctx context.Context, locator string, size int64) (Blob, error) {
	full, err := f.Path(locator)
	if err != nil {
		return nil, err
	}
	return OpenFile(full)
}

// Reads a file whole, capped
func (f *File) Read(ctx context.Context, locator string, max int64) ([]byte, error) {
	full, err := f.Path(locator)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readAllCapped(file, max)
}
