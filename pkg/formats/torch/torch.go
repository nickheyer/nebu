// Package torch reads tensor headers out of torch.save zips, the pickle zip helper nemo uses.
package torch

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"strconv"

	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/pickle"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const maxPickle = 512 << 20

// What a checkpoint's pickle says about its tensors, plus its scalar entries
//
// Shapes runs parallel to Tensors for callers that need dimensions.
type Checkpoint struct {
	Tensors  []*v1.TensorInfo
	Shapes   [][]uint64
	Metadata map[string]string
}

// Reads a torch.save zip through random access, touching only the central
// directory and the pickle
func ReadZip(ra io.ReaderAt, size int64) (*Checkpoint, error) {
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return nil, fmt.Errorf("not a zip checkpoint: %w", err)
	}
	var pkl *zip.File
	for _, f := range zr.File {
		if path.Base(f.Name) == "data.pkl" {
			pkl = f
			break
		}
	}
	if pkl == nil {
		return nil, fmt.Errorf("no data.pkl in checkpoint")
	}
	if pkl.UncompressedSize64 > maxPickle {
		return nil, fmt.Errorf("pickle of %d bytes is too large", pkl.UncompressedSize64)
	}
	rc, err := pkl.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxPickle))
	if err != nil {
		return nil, err
	}
	root, err := pickle.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return fromPickle(root)
}

// Reads tensors out of the root dict, its state_dict entry, or nested dicts
func fromPickle(root any) (*Checkpoint, error) {
	dict, ok := root.(*pickle.Dict)
	if !ok {
		return nil, fmt.Errorf("checkpoint root is %T, not a dict", root)
	}
	ck := &Checkpoint{Metadata: map[string]string{}}
	for _, p := range dict.Pairs {
		if k, ok := p.Key.(string); ok {
			if s, ok := scalar(p.Value); ok {
				ck.Metadata[k] = s
			}
		}
	}
	state := dict
	if sd, ok := dict.Get("state_dict"); ok {
		if d, ok := sd.(*pickle.Dict); ok {
			state = d
		}
	}
	collect(state, "", ck, 0)
	return ck, nil
}

func collect(d *pickle.Dict, prefix string, ck *Checkpoint, depth int) {
	if depth > 4 {
		return
	}
	var nested []pickle.Pair
	for _, p := range d.Pairs {
		name, ok := p.Key.(string)
		if !ok {
			continue
		}
		if t, shape := tensor(prefix+name, p.Value); t != nil {
			ck.Tensors = append(ck.Tensors, t)
			ck.Shapes = append(ck.Shapes, shape)
			continue
		}
		if child, ok := p.Value.(*pickle.Dict); ok {
			nested = append(nested, pickle.Pair{Key: name, Value: child})
		}
	}
	if len(ck.Tensors) > 0 && depth == 0 && len(nested) > 0 {
		// A state dict with tensors at the top does not hide more in children
		return
	}
	for _, p := range nested {
		collect(p.Value.(*pickle.Dict), prefix+p.Key.(string)+".", ck, depth+1)
	}
}

// Recognizes the rebuild calls torch pickles a tensor as, naming it
func tensor(name string, v any) (*v1.TensorInfo, []uint64) {
	obj, ok := v.(*pickle.Object)
	if !ok {
		return nil, nil
	}
	switch obj.Class.Name {
	case "_rebuild_parameter", "_rebuild_parameter_with_state":
		if len(obj.Args) > 0 {
			return tensor(name, obj.Args[0])
		}
		return nil, nil
	case "_rebuild_from_type_v2":
		// (func, new_type, args, state)
		if len(obj.Args) >= 3 {
			if fn, ok := obj.Args[0].(pickle.Global); ok {
				if args, ok := pickle.Items(obj.Args[2]); ok {
					return tensor(name, &pickle.Object{Class: fn, Args: args})
				}
			}
		}
		return nil, nil
	case "_rebuild_tensor_v2", "_rebuild_tensor_v3", "_rebuild_tensor":
	default:
		return nil, nil
	}
	if len(obj.Args) < 3 {
		return nil, nil
	}
	size, ok := pickle.Items(obj.Args[2])
	if !ok {
		return nil, nil
	}
	shape := make([]uint64, 0, len(size))
	for _, d := range size {
		n, ok := pickle.Int(d)
		if !ok || n < 0 {
			return nil, nil
		}
		shape = append(shape, uint64(n))
	}
	dtype := storageDtype(obj.Args[0])
	if obj.Class.Name == "_rebuild_tensor_v3" && len(obj.Args) >= 7 {
		// v3 carries the dtype explicitly after the hooks
		if g, ok := obj.Args[6].(pickle.Global); ok {
			if _, _, known := formats.Dtype(g.Name); known {
				dtype = g.Name
			}
		}
	}
	return formats.Tensor(name, dtype, shape), shape
}

// Names the storage class in a persistent id, empty when Dtype does not know it
func storageDtype(v any) string {
	p, ok := v.(pickle.Persistent)
	if !ok {
		return ""
	}
	id, ok := pickle.Items(p.ID)
	if !ok || len(id) < 2 {
		return ""
	}
	name := ""
	switch t := id[1].(type) {
	case pickle.Global:
		name = t.Name
	case *pickle.Object:
		name = t.Class.Name
	case string:
		name = t
	}
	if _, _, ok := formats.Dtype(name); ok {
		return name
	}
	return ""
}

func scalar(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case int64:
		return strconv.FormatInt(t, 10), true
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	}
	return "", false
}
