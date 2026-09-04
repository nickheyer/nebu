// Package torch reads tensor names, shapes, and storage types out of PyTorch checkpoints.
//
// A torch.save file is a zip archive whose data.pkl pickles the object graph
// with every tensor's storage left out of line, so the pickle alone, read
// through the zip's central directory, describes the whole checkpoint. The
// archive's storage members are never read.
package torch

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/pickle"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const maxPickle = 512 << 20

type reader struct{}

// Builds a PyTorch checkpoint reader
func New(spec *v1.FormatSpec) (formats.Reader, error) {
	return &reader{}, nil
}

func (r *reader) Read(ctx context.Context, open formats.Opener, group *formats.Group) (*v1.RawModel, error) {
	raw := &v1.RawModel{FormatId: group.FormatID, Group: group.Name, Metadata: map[string]string{}}
	for _, a := range group.Weights {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		blob, err := open(ctx, a)
		if err != nil {
			return nil, err
		}
		ck, err := ReadZip(blob, blob.Size())
		blob.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.GetPath(), err)
		}
		for k, v := range ck.Metadata {
			if _, exists := raw.Metadata[k]; !exists {
				raw.Metadata[k] = v
			}
		}
		raw.Tensors = append(raw.Tensors, ck.Tensors...)
	}
	return raw, nil
}

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
	return FromPickle(root)
}

// Reads tensors out of a decoded checkpoint pickle
//
// The state dict is the root, or its state_dict entry when a training
// framework wrapped it, and nested dicts are searched with dotted prefixes
// when neither holds tensors directly.
func FromPickle(root any) (*Checkpoint, error) {
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
		if t, shape := tensor(p.Value); t != nil {
			t.Name = prefix + name
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

// Recognizes the rebuild calls torch pickles a tensor as
func tensor(v any) (*v1.TensorInfo, []uint64) {
	obj, ok := v.(*pickle.Object)
	if !ok {
		return nil, nil
	}
	switch obj.Class.Name {
	case "_rebuild_parameter", "_rebuild_parameter_with_state":
		if len(obj.Args) > 0 {
			return tensor(obj.Args[0])
		}
		return nil, nil
	case "_rebuild_from_type_v2":
		// (func, new_type, args, state)
		if len(obj.Args) >= 3 {
			if fn, ok := obj.Args[0].(pickle.Global); ok {
				if args, ok := pickle.Items(obj.Args[2]); ok {
					return tensor(&pickle.Object{Class: fn, Args: args})
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
	elements := uint64(1)
	shape := make([]uint64, 0, len(size))
	for _, d := range size {
		n, ok := pickle.Int(d)
		if !ok || n < 0 {
			return nil, nil
		}
		elements *= uint64(n)
		shape = append(shape, uint64(n))
	}
	label, width := storageDtype(obj.Args[0])
	if obj.Class.Name == "_rebuild_tensor_v3" && len(obj.Args) >= 7 {
		// v3 carries the dtype explicitly after the hooks
		if g, ok := obj.Args[6].(pickle.Global); ok {
			if l, w, known := Dtype(g.Name); known {
				label, width = l, w
			}
		}
	}
	return &v1.TensorInfo{Dtype: label, Elements: elements, Bytes: uint64(math.Ceil(float64(elements) * width))}, shape
}

// Reads the storage class out of the persistent id torch writes for a storage
func storageDtype(v any) (string, float64) {
	p, ok := v.(pickle.Persistent)
	if !ok {
		return "", 0
	}
	id, ok := pickle.Items(p.ID)
	if !ok || len(id) < 2 {
		return "", 0
	}
	switch t := id[1].(type) {
	case pickle.Global:
		if l, w, ok := Dtype(t.Name); ok {
			return l, w
		}
	case *pickle.Object:
		if l, w, ok := Dtype(t.Class.Name); ok {
			return l, w
		}
	case string:
		if l, w, ok := Dtype(t); ok {
			return l, w
		}
	}
	return "", 0
}

// Maps a torch storage class, torch dtype, or numpy type string onto a label
// and its byte width
func Dtype(name string) (string, float64, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimPrefix(n, "torch.")
	n = strings.TrimSuffix(n, "storage")
	n = strings.TrimLeft(n, "<>|=")
	switch n {
	case "float", "float32", "f4":
		return "F32", 4, true
	case "half", "float16", "f2":
		return "F16", 2, true
	case "bfloat16", "bf16":
		return "BF16", 2, true
	case "double", "float64", "f8":
		return "F64", 8, true
	case "long", "int64", "i8":
		return "I64", 8, true
	case "int", "int32", "i4":
		return "I32", 4, true
	case "short", "int16", "i2":
		return "I16", 2, true
	case "char", "int8", "i1":
		return "I8", 1, true
	case "byte", "uint8", "u1":
		return "U8", 1, true
	case "bool", "b1":
		return "BOOL", 1, true
	case "float8_e4m3fn", "float8_e4m3fnuz":
		return "F8_E4M3", 1, true
	case "float8_e5m2", "float8_e5m2fnuz":
		return "F8_E5M2", 1, true
	case "complexfloat", "complex64", "c8":
		return "C64", 8, true
	case "complexdouble", "complex128", "c16":
		return "C128", 16, true
	case "quint8", "qint8":
		return "Q8", 1, true
	case "qint32":
		return "Q32", 4, true
	}
	return "", 0, false
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
