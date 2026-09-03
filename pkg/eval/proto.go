package eval

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Lowercases an enum value name without its type prefix
func EnumShort(e protoreflect.Enum) string {
	name := string(e.Descriptor().Values().ByNumber(e.Number()).Name())
	name = strings.TrimPrefix(name, snakeUpper(string(e.Descriptor().Name()))+"_")
	return strings.ToLower(name)
}

func snakeUpper(camel string) string {
	var b strings.Builder
	for i, r := range camel {
		if i > 0 && r >= 'A' && r <= 'Z' && camel[i-1] >= 'a' && camel[i-1] <= 'z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

// Flattens decoded JSON into dotted string keys
func Flatten(prefix string, v any, into map[string]string, norm func(string) string) {
	if norm == nil {
		norm = func(s string) string { return s }
	}
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			key := norm(k)
			if prefix != "" {
				key = prefix + "." + key
			}
			Flatten(key, child, into, norm)
		}
	case []any:
		var parts []string
		for i, item := range t {
			switch item.(type) {
			case map[string]any, []any:
				Flatten(prefix+"."+strconv.Itoa(i), item, into, norm)
			default:
				parts = append(parts, Scalar(item))
			}
		}
		into[prefix] = strings.Join(parts, ",")
		into[prefix+".length"] = strconv.Itoa(len(t))
	default:
		into[prefix] = Scalar(t)
	}
}

// Formats a decoded JSON scalar as text
func Scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	}
	return fmt.Sprint(v)
}
