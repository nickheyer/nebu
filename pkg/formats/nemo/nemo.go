// Package nemo reads NeMo checkpoints without fetching weight data. NeMo 1 archives contain
// model_config.yaml and torch, zarr, or distributed weights. NeMo 2 directories contain context/
// configs and distributed weights under weights/.
package nemo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/pickle"
	"github.com/nickheyer/nebu/pkg/formats/torch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
	"sigs.k8s.io/yaml"
)

const (
	maxSmall   = 64 << 20
	metaFile   = ".metadata"
	zarrFile   = ".zarray"
	tarExt     = ".nemo"
	archKey    = "nemo.architecture"
	layersKey  = "nemo.layers"
	backendKey = "nemo.sharded_backend"
)

// Names the model config in either layout
var configNames = map[string]bool{"model_config.yaml": true, "model.yaml": true}

// Reads the headers of a packed or directory checkpoint, shared by both layouts
type reader struct{}

// A tensor with the shape its header gave it, kept until layers are expanded
type shaped struct {
	info  *v1.TensorInfo
	shape []uint64
}

func (r *reader) Read(ctx context.Context, open formats.Opener, group *formats.Group) (*v1.RawModel, error) {
	raw := &v1.RawModel{FormatId: group.FormatID, Group: group.Name, Metadata: map[string]string{}}
	var tensors []shaped
	var err error
	if len(group.Weights) == 1 && strings.EqualFold(path.Ext(group.Weights[0].GetPath()), tarExt) {
		if tensors, err = r.readTar(ctx, open, group.Weights[0], raw); err != nil {
			return nil, fmt.Errorf("%s: %w", group.Weights[0].GetPath(), err)
		}
	} else if tensors, err = r.readDir(ctx, open, group, raw); err != nil {
		return nil, err
	}
	finish(raw, tensors)
	return raw, nil
}

// Reads a directory checkpoint from its attached config and metadata files
func (r *reader) readDir(ctx context.Context, open formats.Opener, group *formats.Group, raw *v1.RawModel) ([]shaped, error) {
	var tensors []shaped
	for _, a := range group.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG] {
		base := path.Base(a.GetPath())
		switch {
		case configNames[base]:
			data, err := formats.ReadAll(ctx, open, a, maxSmall)
			if err == nil {
				err = flattenYAML(data, raw.Metadata)
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", a.GetPath(), err)
			}
		case base == "metadata.json":
			data, err := formats.ReadAll(ctx, open, a, maxSmall)
			if err == nil {
				err = text.FlattenJSON(data, "weights", raw.Metadata)
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", a.GetPath(), err)
			}
		case base == metaFile:
			data, err := formats.ReadAll(ctx, open, a, maxSmall)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", a.GetPath(), err)
			}
			ts, err := distTensors(data)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", a.GetPath(), err)
			}
			tensors = append(tensors, ts...)
		}
	}
	if len(tensors) == 0 {
		// Without the distributed checkpoint index the shard files still say
		// how many bytes there are
		for _, a := range group.Weights {
			tensors = append(tensors, shaped{info: &v1.TensorInfo{Name: a.GetPath(), Bytes: a.GetSizeBytes()}})
		}
	}
	return tensors, nil
}

// Reads a packed checkpoint by walking the tar
func (r *reader) readTar(ctx context.Context, open formats.Opener, a *v1.Artifact, raw *v1.RawModel) ([]shaped, error) {
	blob, err := open(ctx, a)
	if err != nil {
		return nil, err
	}
	defer blob.Close()
	w := newWalker(blob, blob.Size())
	var ckpt *entry
	var zarrs []*entry
	var dist *entry
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e, err := w.next()
		if err != nil {
			return nil, err
		}
		if e == nil {
			break
		}
		if e.dir {
			continue
		}
		name := path.Clean(e.name)
		base := path.Base(name)
		switch {
		case configNames[base]:
			data, err := w.contents(e)
			if err != nil {
				return nil, err
			}
			if err := flattenYAML(data, raw.Metadata); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
		case base == "metadata.json" && strings.Contains(name, "weights"):
			data, err := w.contents(e)
			if err != nil {
				return nil, err
			}
			if err := text.FlattenJSON(data, "weights", raw.Metadata); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
		case base == "model_weights.ckpt":
			ckpt = e
		case base == metaFile:
			dist = e
		case base == zarrFile:
			zarrs = append(zarrs, e)
		}
	}
	switch {
	case dist != nil:
		data, err := w.contents(dist)
		if err != nil {
			return nil, err
		}
		tensors, err := distTensors(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dist.name, err)
		}
		return tensors, nil
	case len(zarrs) > 0:
		tensors := make([]shaped, 0, len(zarrs))
		for _, e := range zarrs {
			data, err := w.contents(e)
			if err != nil {
				return nil, err
			}
			t, err := zarrTensor(path.Clean(e.name), data)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", e.name, err)
			}
			tensors = append(tensors, t)
		}
		return tensors, nil
	case ckpt != nil:
		ck, err := torch.ReadZip(io.NewSectionReader(blob, ckpt.offset, ckpt.size), ckpt.size)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ckpt.name, err)
		}
		for k, v := range ck.Metadata {
			if _, exists := raw.Metadata["checkpoint."+k]; !exists {
				raw.Metadata["checkpoint."+k] = v
			}
		}
		tensors := make([]shaped, 0, len(ck.Tensors))
		for i, t := range ck.Tensors {
			tensors = append(tensors, shaped{info: t, shape: ck.Shapes[i]})
		}
		return tensors, nil
	}
	return nil, fmt.Errorf("no model weights found in the archive")
}

// Derives the architecture name and expands stacked layers once metadata is in
func finish(raw *v1.RawModel, tensors []shaped) {
	if target := firstOf(raw.Metadata, "_target_", "target"); target != "" {
		raw.Metadata[archKey] = archName(target)
	}
	layers := 0
	if s := firstOf(raw.Metadata, "config.num_layers", "num_layers"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			layers = n
		}
	}
	raw.Tensors = expandLayers(tensors, layers)
	if layers > 0 {
		raw.Metadata[layersKey] = strconv.Itoa(layers)
	}
	if b, ok := raw.Metadata["weights.sharded_backend"]; ok {
		raw.Metadata[backendKey] = b
	}
}

// Turns a class path such as nemo.collections.llm.gpt.model.llama.LlamaModel into llama
func archName(target string) string {
	name := target[strings.LastIndex(target, ".")+1:]
	for _, suffix := range []string{"Model", "Config"} {
		name = strings.TrimSuffix(name, suffix)
	}
	name = strings.TrimPrefix(name, "Megatron")
	return strings.ToLower(name)
}

// Splits Megatron tensors stacked on their first dimension into per-layer entries, preserving total
// size.
func expandLayers(tensors []shaped, layers int) []*v1.TensorInfo {
	out := make([]*v1.TensorInfo, 0, len(tensors))
	for _, s := range tensors {
		t := s.info
		head, tail, ok := stackedName(t.GetName())
		n := layers
		if len(s.shape) > 0 {
			if n == 0 || int(s.shape[0]) == n {
				n = int(s.shape[0])
			} else {
				n = 0
			}
		}
		if !ok || n <= 0 || t.GetElements()%uint64(n) != 0 {
			out = append(out, t)
			continue
		}
		for i := 0; i < n; i++ {
			out = append(out, &v1.TensorInfo{
				Name:     head + strconv.Itoa(i) + "." + tail,
				Dtype:    t.GetDtype(),
				Bytes:    t.GetBytes() / uint64(n),
				Elements: t.GetElements() / uint64(n),
			})
		}
	}
	return out
}

// Splits decoder.layers.<parameter> into the layer prefix and parameter name. Rejects names with an
// existing layer index.
func stackedName(name string) (head, tail string, ok bool) {
	seg := strings.Split(name, ".")
	for i := 0; i+1 < len(seg); i++ {
		if (seg[i] == "decoder" || seg[i] == "encoder") && seg[i+1] == "layers" && i+2 < len(seg) {
			next := seg[i+2]
			if next == "" || next[0] >= '0' && next[0] <= '9' {
				return "", "", false
			}
			return strings.Join(seg[:i+2], ".") + ".", strings.Join(seg[i+2:], "."), true
		}
	}
	return "", "", false
}

// Reads tensors out of a torch distributed checkpoint's .metadata pickle
func distTensors(data []byte) ([]shaped, error) {
	root, err := pickle.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	obj, ok := root.(*pickle.Object)
	if !ok {
		return nil, fmt.Errorf("metadata root is %T", root)
	}
	state, ok := obj.State.(*pickle.Dict)
	if !ok {
		return nil, fmt.Errorf("metadata has no state")
	}
	entries, _ := state.Get("state_dict_metadata")
	dict, ok := entries.(*pickle.Dict)
	if !ok {
		return nil, fmt.Errorf("metadata has no state_dict_metadata")
	}
	var out []shaped
	for _, p := range dict.Pairs {
		name, ok := p.Key.(string)
		if !ok {
			continue
		}
		t, ok := p.Value.(*pickle.Object)
		if !ok || t.Class.Name != "TensorStorageMetadata" {
			continue
		}
		st, ok := t.State.(*pickle.Dict)
		if !ok {
			continue
		}
		sizeVal, _ := st.Get("size")
		dims, _ := pickle.Items(sizeVal)
		shape := make([]uint64, 0, len(dims))
		for _, d := range dims {
			n, ok := pickle.Int(d)
			if !ok || n < 0 {
				shape = nil
				break
			}
			shape = append(shape, uint64(n))
		}
		props, _ := st.Get("properties")
		out = append(out, shaped{info: formats.Tensor(name, propertiesDtype(props), shape), shape: shape})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("metadata lists no tensors")
	}
	return out, nil
}

// Extracts the torch dtype from TensorProperties dict state, slot state, or constructor arguments.
func propertiesDtype(v any) string {
	obj, ok := v.(*pickle.Object)
	if !ok {
		return ""
	}
	if st, ok := obj.State.(*pickle.Dict); ok {
		if d, ok := st.Get("dtype"); ok {
			if g, ok := d.(pickle.Global); ok {
				return g.Name
			}
		}
	}
	for _, candidates := range [][]any{stateItems(obj.State), obj.Args} {
		for _, c := range candidates {
			if g, ok := c.(pickle.Global); ok {
				if _, _, known := formats.Dtype(g.Name); known {
					return g.Name
				}
			}
		}
	}
	return ""
}

// Flattens a slot state tuple, including the (dict, slots) pair form
func stateItems(state any) []any {
	items, ok := pickle.Items(state)
	if !ok {
		return nil
	}
	var out []any
	for _, it := range items {
		if d, ok := it.(*pickle.Dict); ok {
			for _, p := range d.Pairs {
				out = append(out, p.Value)
			}
			continue
		}
		out = append(out, it)
	}
	return out
}

// Reads one zarr array header, named by the directory that holds it
func zarrTensor(file string, data []byte) (shaped, error) {
	var arr struct {
		Shape []uint64 `json:"shape"`
		Dtype string   `json:"dtype"`
	}
	if err := json.Unmarshal(data, &arr); err != nil {
		return shaped{}, err
	}
	name := path.Base(path.Dir(file))
	return shaped{info: formats.Tensor(name, arr.Dtype, arr.Shape), shape: arr.Shape}, nil
}

func flattenYAML(data []byte, into map[string]string) error {
	js, err := yaml.YAMLToJSON(data)
	if err != nil {
		return err
	}
	return text.FlattenJSON(js, "", into)
}

func firstOf(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(m[k]); v != "" {
			return v
		}
	}
	return ""
}
