// Package safetensors reads shard headers and config files.
package safetensors

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
	"golang.org/x/sync/errgroup"
)

const (
	maxHeader    = 256 << 20
	maxConfig    = 16 << 20
	metadataKey  = "__metadata__"
	shardReaders = 8
)

type tensorHeader struct {
	Dtype       string    `json:"dtype"`
	Shape       []uint64  `json:"shape"`
	DataOffsets [2]uint64 `json:"data_offsets"`
}

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
	var lenBuf [8]byte
	if _, err := blob.ReadAt(lenBuf[:], 0); err != nil {
		return err
	}
	n := binary.LittleEndian.Uint64(lenBuf[:])
	if n > maxHeader || int64(n)+8 > blob.Size() {
		return fmt.Errorf("implausible header length %d", n)
	}
	buf := make([]byte, n)
	if _, err := blob.ReadAt(buf, 8); err != nil && err != io.EOF {
		return err
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(buf, &entries); err != nil {
		return err
	}
	for name, msg := range entries {
		if name == metadataKey {
			var meta map[string]string
			if err := json.Unmarshal(msg, &meta); err == nil {
				for k, v := range meta {
					raw.Metadata[metadataKey+"."+k] = v
				}
			}
			continue
		}
		var th tensorHeader
		if err := json.Unmarshal(msg, &th); err != nil {
			return fmt.Errorf("tensor %s: %w", name, err)
		}
		raw.Tensors = append(raw.Tensors, &v1.TensorInfo{
			Name:     name,
			Dtype:    th.Dtype,
			Bytes:    th.DataOffsets[1] - th.DataOffsets[0],
			Elements: formats.Elements(th.Shape),
		})
	}
	return nil
}
