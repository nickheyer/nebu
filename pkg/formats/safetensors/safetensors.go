// Package safetensors reads shard headers and config files.
package safetensors

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	"github.com/nickheyer/nebu/pkg/eval"
	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	maxHeader   = 256 << 20
	maxConfig   = 16 << 20
	metadataKey = "__metadata__"
)

type reader struct{}

// Builds a safetensors reader
func New(spec *v1.FormatSpec) (formats.Reader, error) {
	return &reader{}, nil
}

type tensorHeader struct {
	Dtype       string    `json:"dtype"`
	Shape       []uint64  `json:"shape"`
	DataOffsets [2]uint64 `json:"data_offsets"`
}

func (r *reader) Read(ctx context.Context, open formats.Opener, group *formats.Group) (*v1.RawModel, error) {
	raw := &v1.RawModel{FormatId: group.FormatID, Group: group.Name, Metadata: map[string]string{}}
	for _, a := range group.Weights {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := r.readShard(ctx, open, a, raw); err != nil {
			return nil, fmt.Errorf("%s: %w", a.GetPath(), err)
		}
	}
	for _, a := range group.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG] {
		data, err := formats.ReadAll(ctx, open, a, maxConfig)
		if err == nil {
			err = eval.FlattenJSON(data, "", raw.Metadata, nil)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.GetPath(), err)
		}
	}
	return raw, nil
}

func (r *reader) readShard(ctx context.Context, open formats.Opener, a *v1.Artifact, raw *v1.RawModel) error {
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
