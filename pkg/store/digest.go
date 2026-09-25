package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"sort"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A stable digest of a descriptor: its populated fields written in field number order, maps by
// key, so the same descriptor digests the same on every member whatever build serialized it.
// Members advertise it beside each stored model and pullers check the manifest they are handed
// against it.
func DescriptorDigest(d *v1.Descriptor) string {
	if d == nil {
		return ""
	}
	h := sha256.New()
	canonMessage(h, d.ProtoReflect())
	return hex.EncodeToString(h.Sum(nil))
}

func canonMessage(w io.Writer, m protoreflect.Message) {
	var fields []protoreflect.FieldDescriptor
	m.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		fields = append(fields, fd)
		return true
	})
	sort.Slice(fields, func(i, j int) bool { return fields[i].Number() < fields[j].Number() })
	for _, fd := range fields {
		fmt.Fprintf(w, "%d:", fd.Number())
		v := m.Get(fd)
		switch {
		case fd.IsList():
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				canonValue(w, fd, l.Get(i))
			}
		case fd.IsMap():
			mp := v.Map()
			var keys []protoreflect.MapKey
			mp.Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
				keys = append(keys, k)
				return true
			})
			sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
			for _, k := range keys {
				fmt.Fprintf(w, "%q=", k.String())
				canonValue(w, fd.MapValue(), mp.Get(k))
			}
		default:
			canonValue(w, fd, v)
		}
		io.WriteString(w, ";")
	}
}

func canonValue(w io.Writer, fd protoreflect.FieldDescriptor, v protoreflect.Value) {
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		io.WriteString(w, "{")
		canonMessage(w, v.Message())
		io.WriteString(w, "}")
	case protoreflect.BytesKind:
		fmt.Fprintf(w, "%x", v.Bytes())
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		fmt.Fprintf(w, "%x", math.Float64bits(v.Float()))
	case protoreflect.StringKind:
		fmt.Fprintf(w, "%q", v.String())
	default:
		fmt.Fprintf(w, "%v", v.Interface())
	}
	io.WriteString(w, ",")
}
