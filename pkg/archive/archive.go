// Package archive extracts tar and zip archives safely.
package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Extracts a tar, tar.gz, or zip archive, rejecting traversal
func Extract(archive, dir string) error {
	switch {
	case strings.HasSuffix(archive, ".tar.gz") || strings.HasSuffix(archive, ".tgz"):
		return extractTar(archive, dir, true)
	case strings.HasSuffix(archive, ".tar"):
		return extractTar(archive, dir, false)
	case strings.HasSuffix(archive, ".zip"):
		return extractZip(archive, dir)
	}
	return sniff(archive, dir)
}

// Extracts by content when the name carries no usable extension
func sniff(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	head := make([]byte, 4)
	n, _ := io.ReadFull(f, head)
	f.Close()
	switch {
	case n >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		return extractTar(archive, dir, true)
	case n >= 4 && string(head) == "PK\x03\x04":
		return extractZip(archive, dir)
	}
	return fmt.Errorf("unsupported archive %s", filepath.Base(archive))
}

// Returns the single top level directory, or dir itself
func Root(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return dir
	}
	return filepath.Join(dir, entries[0].Name())
}

// Reports whether path stays under dir
func Within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && !strings.HasPrefix(rel, "..")
}

func target(dir, name string) (string, error) {
	clean := filepath.Join(dir, filepath.FromSlash(name))
	if !Within(dir, clean) {
		return "", fmt.Errorf("archive entry %q escapes %s", name, dir)
	}
	if err := noSymlinkParents(dir, clean); err != nil {
		return "", err
	}
	return clean, nil
}

// Refuses a path whose existing parents include a symlink, which an earlier entry could have planted
func noSymlinkParents(dir, path string) error {
	rel, err := filepath.Rel(dir, filepath.Dir(path))
	if err != nil {
		return err
	}
	cur := dir
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive entry %q sits under symlink %s", path, cur)
		}
	}
	return nil
}

// Refuses link targets that resolve outside dir
func checkLink(dir, path, linkname string) error {
	if filepath.IsAbs(linkname) {
		return fmt.Errorf("archive symlink %q points at an absolute path", linkname)
	}
	if !Within(dir, filepath.Join(filepath.Dir(path), filepath.FromSlash(linkname))) {
		return fmt.Errorf("archive symlink %q escapes %s", linkname, dir)
	}
	return nil
}

func extractTar(archive, dir string, gzipped bool) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if gzipped {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		path, err := target(dir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := WriteFile(path, tr, os.FileMode(hdr.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := checkLink(dir, path, hdr.Linkname); err != nil {
				return err
			}
			os.Remove(path)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, path); err != nil {
				return err
			}
		}
	}
}

func extractZip(archive, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, entry := range zr.File {
		path, err := target(dir, entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return err
		}
		err = WriteFile(path, rc, entry.Mode()&0o777)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// Writes r to path with mode, 0644 when unset, never through a symlink
func WriteFile(path string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Never write through a link left by an earlier entry
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		os.Remove(path)
	}
	if mode == 0 {
		mode = 0o644
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
