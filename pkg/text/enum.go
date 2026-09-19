// Package text provides enum labels, numeric and version parsing, and JSON flattening.
package text

import (
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Lowercases an enum value name without its type prefix, POOL_KIND_HOST reading as host
func Enum(e protoreflect.Enum) string {
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
