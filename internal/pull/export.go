package pull

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/mirror"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/transfer"
)

const kindExport = "export"

// Starts a task copying stored models into a mirror layout
func (p *Puller) Export(ctx context.Context, req *v1.ExportRequest) (*v1.Task, error) {
	if req.GetDir() == "" {
		return nil, fmt.Errorf("export needs a directory")
	}
	dir, err := filepath.Abs(req.GetDir())
	if err != nil {
		return nil, err
	}
	manifests, err := p.Store.ListManifests()
	if err != nil {
		return nil, err
	}
	var selected []*v1.StoredModel
	for _, m := range manifests {
		if req.GetSourceId() != "" && m.GetSourceId() != req.GetSourceId() {
			continue
		}
		if req.GetRepo() != "" && m.GetRepo() != req.GetRepo() {
			continue
		}
		if req.GetGroup() != "" && m.GetGroup() != req.GetGroup() {
			continue
		}
		selected = append(selected, m)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("%w: nothing matches the export request", store.ErrNotStored)
	}
	title := fmt.Sprintf("export %d models to %s", len(selected), dir)
	return p.Tasks.Start(kindExport, title, map[string]string{"dir": dir}, func(ctx context.Context, h *tasks.Handle) error {
		return p.export(ctx, h, selected, dir)
	}), nil
}

func (p *Puller) export(ctx context.Context, h *tasks.Handle, models []*v1.StoredModel, dir string) error {
	// Nothing is collected or evicted while blobs are being copied out
	defer p.Store.Hold()()
	var total uint64
	for _, m := range models {
		total += m.GetBytes()
	}
	h.Progress(0, total, "copying")
	for _, m := range models {
		repoDir := filepath.Join(dir, filepath.FromSlash(m.GetRepo()))
		if rel, err := filepath.Rel(dir, repoDir); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("repo %q escapes %s", m.GetRepo(), dir)
		}
		index, err := mirror.ReadIndex(filepath.Join(repoDir, mirror.IndexFile))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if index == nil {
			index = &mirror.Index{}
		}
		index.Repo, index.Commit, index.Revision = m.GetRepo(), m.GetCommit(), m.GetRevision()
		for _, sa := range m.GetArtifacts() {
			if err := ctx.Err(); err != nil {
				return err
			}
			a := sa.GetArtifact()
			dest := filepath.Join(repoDir, filepath.FromSlash(a.GetPath()))
			if rel, err := filepath.Rel(repoDir, dest); err != nil || strings.HasPrefix(rel, "..") {
				return fmt.Errorf("artifact %q escapes %s", a.GetPath(), repoDir)
			}
			h.Message("copying " + a.GetPath())
			if err := placeFile(p.Store.BlobPath(sa.GetDigest()), dest, func(d int64) { h.Add(d) }, int64(a.GetSizeBytes()), store.Hex(sa.GetDigest())); err != nil {
				return fmt.Errorf("%s: %w", a.GetPath(), err)
			}
			index.Put(mirror.File{Path: a.GetPath(), Size: a.GetSizeBytes(), Sha256: store.Hex(sa.GetDigest())})
		}
		index.UpdatedAt = time.Now().UTC()
		if err := mirror.WriteIndex(filepath.Join(repoDir, mirror.IndexFile), index); err != nil {
			return err
		}
		h.Logf("exported %s %s to %s", m.GetRepo(), m.GetGroup(), repoDir)
	}
	if err := writeRootIndex(dir); err != nil {
		return err
	}
	h.Progress(total, total, "done")
	return nil
}

// Hard links the blob into place, copying when linking fails
func placeFile(src, dest string, progress func(int64), size int64, hexDigest string) error {
	if info, err := os.Stat(dest); err == nil && info.Size() == size {
		// The same inode is the blob itself, anything else has to hash to it
		if blob, err := os.Stat(src); err == nil && os.SameFile(info, blob) {
			progress(size)
			return nil
		}
		if got, err := transfer.HashFile(dest, progress); err == nil && got == hexDigest {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	os.Remove(dest)
	if err := os.Link(src, dest); err == nil {
		progress(size)
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dest + ".partial"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(tmp)
				return werr
			}
			progress(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(tmp)
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// Lists every repo index under dir into the root index
func writeRootIndex(dir string) error {
	var repos []mirror.RepoEntry
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != mirror.IndexFile || filepath.Dir(path) == dir {
			return err
		}
		index, err := mirror.ReadIndex(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, filepath.Dir(path))
		repos = append(repos, mirror.RepoEntry{Repo: filepath.ToSlash(rel), Commit: index.Commit, Files: len(index.Files), UpdatedAt: index.UpdatedAt})
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Repo < repos[j].Repo })
	data, err := json.MarshalIndent(mirror.Root{Repos: repos}, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, mirror.IndexFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, mirror.IndexFile))
}
