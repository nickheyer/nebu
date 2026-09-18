// Package safetensors reads shard headers and config files.
package safetensors

import (
	"context"
	"fmt"

	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
	"golang.org/x/sync/errgroup"
)

const (
	maxConfig    = 16 << 20
	shardReaders = 8
)

func (f Format) Read(ctx context.Context, open formats.Opener, group *formats.Group) (*v1.RawModel, error) {
	raw := &v1.RawModel{FormatId: group.FormatID, Group: group.Name, Metadata: map[string]string{}}
	// A checkpoint of many shards is read several at a time, the tensors kept in shard order
	shards := make([]*v1.RawModel, len(group.Weights))
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(shardReaders)
	for i, a := range group.Weights {
		eg.Go(func() error {
			part := &v1.RawModel{Metadata: map[string]string{}}
			if err := readShard(gctx, open, a, part); err != nil {
				return fmt.Errorf("%s: %w", a.GetPath(), err)
			}
			shards[i] = part
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	for _, part := range shards {
		raw.Tensors = append(raw.Tensors, part.Tensors...)
		for k, v := range part.Metadata {
			raw.Metadata[k] = v
		}
	}
	for _, a := range group.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG] {
		data, err := formats.ReadAll(ctx, open, a, maxConfig)
		if err == nil {
			err = text.FlattenJSON(data, "", raw.Metadata)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.GetPath(), err)
		}
	}
	return raw, nil
}

func readShard(ctx context.Context, open formats.Opener, a *v1.Artifact, raw *v1.RawModel) error {
	blob, err := open(ctx, a)
	if err != nil {
		return err
	}
	defer blob.Close()
	metadata, tensors, err := formats.SafetensorsHeader(blob, blob.Size())
	if err != nil {
		return err
	}
	for k, v := range metadata {
		raw.Metadata[k] = v
	}
	raw.Tensors = append(raw.Tensors, tensors...)
	return nil
}
