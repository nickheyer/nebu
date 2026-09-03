// Package pull moves weight groups from sources into the store.
package pull

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/transfer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	kindPull   = "pull"
	kindVerify = "verify"
)

// Orchestrates resolve, fetch, link, and manifest writes
type Puller struct {
	Inspector *inspect.Inspector
	Store     *store.Store
	Fetcher   *transfer.Fetcher
	Tasks     *tasks.Manager
	Log       *slog.Logger
}

// Validates the request and starts a pull task
func (p *Puller) Pull(ctx context.Context, req *v1.PullRequest) (*v1.Task, error) {
	src, model, err := p.Inspector.Resolve(ctx, req.GetSourceId(), req.GetRepo(), req.GetRevision())
	if err != nil {
		return nil, err
	}
	g, err := formats.FindGroup(formats.Groups(model), req.GetGroup())
	if err != nil {
		return nil, err
	}
	labels := map[string]string{"source": model.GetSourceId(), "repo": model.GetRepo(), "group": g.Name}
	title := fmt.Sprintf("pull %s %s", model.GetRepo(), g.Name)
	return p.Tasks.Start(kindPull, title, labels, func(ctx context.Context, h *tasks.Handle) error {
		return p.run(ctx, h, src, model, g)
	}), nil
}

func (p *Puller) run(ctx context.Context, h *tasks.Handle, src sources.Source, model *v1.Model, g *formats.Group) error {
	artifacts := append([]*v1.Artifact(nil), g.Weights...)
	for _, role := range []v1.ArtifactRole{v1.ArtifactRole_ARTIFACT_ROLE_CONFIG, v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER, v1.ArtifactRole_ARTIFACT_ROLE_TEMPLATE, v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR} {
		artifacts = append(artifacts, g.Files[role]...)
	}
	var total uint64
	for _, a := range artifacts {
		total += a.GetSizeBytes()
	}
	h.Progress(0, total, "resolving")
	unlock := p.Store.Lock(store.Key(model.GetSourceId(), model.GetRepo(), g.Name))
	defer unlock()
	stored := &v1.StoredModel{
		SourceId: model.GetSourceId(),
		Repo:     model.GetRepo(),
		Revision: model.GetRevision(),
		Commit:   model.GetCommit(),
		Group:    g.Name,
		FormatId: g.FormatID,
	}
	if d, err := p.Inspector.Describe(ctx, src, model, g); err != nil {
		h.Logf("descriptor unavailable: %v", err)
	} else {
		stored.Descriptor_ = d
	}
	dir := path.Dir(g.Weights[0].GetPath())
	for _, a := range artifacts {
		if err := ctx.Err(); err != nil {
			return err
		}
		digest, err := p.fetch(ctx, h, src, model, a)
		if err != nil {
			return fmt.Errorf("%s: %w", a.GetPath(), err)
		}
		rel := a.GetPath()
		if dir != "." {
			rel = strings.TrimPrefix(rel, dir+"/")
		}
		link, err := p.Store.Link(model.GetSourceId(), model.GetRepo(), g.Name, rel, digest)
		if err != nil {
			return err
		}
		stored.Artifacts = append(stored.Artifacts, &v1.StoredArtifact{Artifact: a, Digest: digest, Path: link})
		stored.Bytes += a.GetSizeBytes()
	}
	stored.Path, _ = p.Store.GroupDir(model.GetSourceId(), model.GetRepo(), g.Name)
	stored.PulledAt = timestamppb.Now()
	if err := p.Store.WriteManifest(stored); err != nil {
		return err
	}
	h.Progress(total, total, "done")
	h.Logf("stored at %s", stored.Path)
	return nil
}

// Ensures one artifact's blob exists and returns its digest
func (p *Puller) fetch(ctx context.Context, h *tasks.Handle, src sources.Source, model *v1.Model, a *v1.Artifact) (string, error) {
	size := int64(a.GetSizeBytes())
	if a.GetSha256() != "" && p.Store.HasBlob(store.Digest(a.GetSha256())) {
		h.Add(size)
		h.Logf("%s already stored", a.GetPath())
		return store.Digest(a.GetSha256()), nil
	}
	blob, err := src.Open(ctx, model, a)
	if err != nil {
		return "", err
	}
	defer blob.Close()
	if local, ok := blob.(sources.Pather); ok {
		h.Message("hashing " + a.GetPath())
		hexDigest, err := transfer.HashFile(local.Path(), func(d int64) { h.Add(d) })
		if err != nil {
			return "", err
		}
		if a.GetSha256() != "" && hexDigest != a.GetSha256() {
			return "", fmt.Errorf("%w: got %s want %s", transfer.ErrDigestMismatch, hexDigest, a.GetSha256())
		}
		digest := store.Digest(hexDigest)
		unlock := p.Store.Lock("blob:" + digest)
		defer unlock()
		if p.Store.HasBlob(digest) {
			h.Logf("%s already stored", a.GetPath())
			return digest, nil
		}
		if err := p.Store.Adopt(local.Path(), digest); err != nil {
			return "", err
		}
		h.Logf("%s adopted as %s", a.GetPath(), digest)
		return digest, nil
	}
	key := partialKey(model, a)
	unlock := p.Store.Lock("blob:" + key)
	defer unlock()
	if a.GetSha256() != "" && p.Store.HasBlob(store.Digest(a.GetSha256())) {
		h.Add(size)
		return store.Digest(a.GetSha256()), nil
	}
	h.Message("downloading " + a.GetPath())
	hexDigest, err := p.Fetcher.Fetch(ctx, blob, p.Store.PartialPath(key), a.GetSha256(), func(d int64) { h.Add(d) })
	if err != nil {
		return "", err
	}
	digest := store.Digest(hexDigest)
	if err := p.Store.Commit(p.Store.PartialPath(key), digest); err != nil {
		return "", err
	}
	h.Logf("%s verified %s", a.GetPath(), digest)
	return digest, nil
}

func partialKey(model *v1.Model, a *v1.Artifact) string {
	if a.GetSha256() != "" {
		return "sha256-" + strings.ToLower(a.GetSha256())
	}
	sum := sha256.Sum256([]byte(model.GetSourceId() + "\x00" + model.GetRepo() + "\x00" + a.GetPath()))
	return "pending-" + hex.EncodeToString(sum[:16])
}

// Starts a task that rehashes stored blobs and drops corrupt ones
func (p *Puller) Verify(ctx context.Context, req *v1.VerifyRequest) (*v1.Task, error) {
	manifests, err := p.Store.ListManifests()
	if err != nil {
		return nil, err
	}
	var selected []*v1.StoredModel
	for _, m := range manifests {
		if req.GetRepo() != "" && m.GetRepo() != req.GetRepo() {
			continue
		}
		if req.GetSourceId() != "" && m.GetSourceId() != req.GetSourceId() {
			continue
		}
		if req.GetGroup() != "" && m.GetGroup() != req.GetGroup() {
			continue
		}
		selected = append(selected, m)
	}
	if req.GetRepo() != "" && len(selected) == 0 {
		return nil, fmt.Errorf("%w: %s", store.ErrNotStored, req.GetRepo())
	}
	title := fmt.Sprintf("verify %d models", len(selected))
	return p.Tasks.Start(kindVerify, title, nil, func(ctx context.Context, h *tasks.Handle) error {
		return p.verify(ctx, h, selected)
	}), nil
}

func (p *Puller) verify(ctx context.Context, h *tasks.Handle, models []*v1.StoredModel) error {
	var total uint64
	for _, m := range models {
		total += m.GetBytes()
	}
	h.Progress(0, total, "hashing")
	bad := 0
	for _, m := range models {
		for _, sa := range m.GetArtifacts() {
			if err := ctx.Err(); err != nil {
				return err
			}
			h.Message("hashing " + sa.GetArtifact().GetPath())
			if !p.Store.HasBlob(sa.GetDigest()) {
				bad++
				h.Logf("%s %s missing blob %s", m.GetRepo(), sa.GetArtifact().GetPath(), sa.GetDigest())
				h.Add(int64(sa.GetArtifact().GetSizeBytes()))
				continue
			}
			got, err := transfer.HashFile(p.Store.BlobPath(sa.GetDigest()), func(d int64) { h.Add(d) })
			if err != nil {
				return err
			}
			if store.Digest(got) != sa.GetDigest() {
				bad++
				h.Logf("%s %s corrupt, removing blob %s", m.GetRepo(), sa.GetArtifact().GetPath(), sa.GetDigest())
				if err := p.Store.RemoveBlob(sa.GetDigest()); err != nil {
					return err
				}
			}
		}
	}
	h.Progress(total, total, "done")
	if bad > 0 {
		return fmt.Errorf("%d artifacts failed verification, pull again to repair", bad)
	}
	return nil
}
