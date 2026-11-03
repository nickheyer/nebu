package requestcontrol

import (
	"net/http"
	"reflect"
	"slices"
)

// A copy of a struct whose maps, slices and struct pointers, at any depth, share no storage with the source
func isolate[T any](src *T) *T {
	cp := *src
	isolateValue(reflect.ValueOf(&cp).Elem())
	return &cp
}

func isolateValue(v reflect.Value) {
	switch {
	case v.Kind() == reflect.Struct && allExported(v.Type()):
		for i := range v.NumField() {
			isolateValue(v.Field(i))
		}
	case v.Kind() == reflect.Pointer && !v.IsNil() && v.Elem().Kind() == reflect.Struct && allExported(v.Elem().Type()):
		cp := reflect.New(v.Elem().Type())
		cp.Elem().Set(v.Elem())
		isolateValue(cp.Elem())
		v.Set(cp)
	case v.Kind() == reflect.Map && !v.IsNil():
		m := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			m.SetMapIndex(it.Key(), it.Value())
		}
		v.Set(m)
	case v.Kind() == reflect.Slice && !v.IsNil():
		s := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		reflect.Copy(s, v)
		v.Set(s)
	}
}

// Applies to dst every change from base to got, recursing through nested structs and merging maps key by key
func merge[T any](dst, base, got *T) {
	mergeValue(reflect.ValueOf(dst).Elem(), reflect.ValueOf(base).Elem(), reflect.ValueOf(got).Elem())
}

func mergeValue(dst, base, got reflect.Value) {
	switch {
	case dst.Kind() == reflect.Struct && allExported(dst.Type()):
		for i := range dst.NumField() {
			mergeValue(dst.Field(i), base.Field(i), got.Field(i))
		}
	case dst.Kind() == reflect.Map:
		mergeMap(dst, base, got)
	case !reflect.DeepEqual(base.Interface(), got.Interface()):
		dst.Set(got)
	}
}

// Whether every field of a struct type is exported, so its fields may be read and set one by one
func allExported(t reflect.Type) bool {
	for i := range t.NumField() {
		if !t.Field(i).IsExported() {
			return false
		}
	}
	return true
}

// Sets in dst the keys got added or changed from base and deletes the keys got removed
func mergeMap(dst, base, got reflect.Value) {
	for it := got.MapRange(); it.Next(); {
		k, v := it.Key(), it.Value()
		if bv := base.MapIndex(k); bv.IsValid() && reflect.DeepEqual(bv.Interface(), v.Interface()) {
			continue
		}
		if dst.IsNil() {
			dst.Set(reflect.MakeMap(dst.Type()))
		}
		dst.SetMapIndex(k, v)
	}
	if dst.IsNil() {
		return
	}
	for it := base.MapRange(); it.Next(); {
		if !got.MapIndex(it.Key()).IsValid() {
			dst.SetMapIndex(it.Key(), reflect.Value{})
		}
	}
}

// Sets in dst the headers got added or changed from base and deletes the headers got removed
func mergeHeader(dst, base, got http.Header) {
	for k, v := range got {
		if bv, ok := base[k]; !ok || !slices.Equal(bv, v) {
			dst[k] = v
		}
	}
	for k := range base {
		if _, ok := got[k]; !ok {
			delete(dst, k)
		}
	}
}
