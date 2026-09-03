package installs

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

// Extracts a tar.gz or zip archive under dir, rejecting traversal
func extract(archive, dir string) error {
	switch {
	case strings.HasSuffix(archive, ".tar.gz") || strings.HasSuffix(archive, ".tgz"):
		return extractTar(archive, dir)
	case strings.HasSuffix(archive, ".zip"):
		return extractZip(archive, dir)
	}
	return fmt.Errorf("unsupported archive %s", filepath.Base(archive))
}

func target(dir, name string) (string, error) {
	clean := filepath.Join(dir, filepath.FromSlash(name))
	if rel, err := filepath.Rel(dir, clean); err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("archive entry %q escapes %s", name, dir)
	}
	return clean, nil
}

func extractTar(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
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
			if err := writeFile(path, tr, os.FileMode(hdr.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeSymlink:
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
		err = writeFile(path, rc, entry.Mode()&0o777)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
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
