package formats

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	maxSafetensorsHeader = 256 << 20
	safetensorsMetadata  = "__metadata__"
)

type safetensorsTensor struct {
	Dtype       string    `json:"dtype"`
	Shape       []uint64  `json:"shape"`
	DataOffsets [2]uint64 `json:"data_offsets"`
}

// SafetensorsHeader reads the tensor table and __metadata__ without fetching weight data.
func SafetensorsHeader(ra io.ReaderAt, size int64) (map[string]string, []*v1.TensorInfo, error) {
	var lenBuf [8]byte
	if _, err := ra.ReadAt(lenBuf[:], 0); err != nil {
		return nil, nil, err
	}
	n := binary.LittleEndian.Uint64(lenBuf[:])
	if n > maxSafetensorsHeader || int64(n)+8 > size {
		return nil, nil, fmt.Errorf("implausible header length %d", n)
	}
	buf := make([]byte, n)
	if _, err := ra.ReadAt(buf, 8); err != nil && err != io.EOF {
		return nil, nil, err
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(buf, &entries); err != nil {
		return nil, nil, err
	}
	metadata := map[string]string{}
	var tensors []*v1.TensorInfo
	for name, msg := range entries {
		if name == safetensorsMetadata {
			var meta map[string]string
			if err := json.Unmarshal(msg, &meta); err == nil {
				for k, v := range meta {
					metadata[safetensorsMetadata+"."+k] = v
				}
			}
			continue
		}
		var th safetensorsTensor
		if err := json.Unmarshal(msg, &th); err != nil {
			return nil, nil, fmt.Errorf("tensor %s: %w", name, err)
		}
		tensors = append(tensors, &v1.TensorInfo{
			Name:     name,
			Dtype:    th.Dtype,
			Bytes:    th.DataOffsets[1] - th.DataOffsets[0],
			Elements: Elements(th.Shape),
			Shape:    th.Shape,
		})
	}
	return metadata, tensors, nil
}
