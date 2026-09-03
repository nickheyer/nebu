package db

import (
	"database/sql"
	"strings"
	"time"
	"unicode"

	"github.com/nickheyer/nebu/pkg/eval"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Stores a timestamp as RFC3339 text, NULL when unset
func timeCol(ts *timestamppb.Timestamp) sql.NullString {
	if ts == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: stamp(ts.AsTime()), Valid: true}
}

// Reads a timestamp column, nil when NULL
func timeVal(s sql.NullString) *timestamppb.Timestamp {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return nil
	}
	return timestamppb.New(t)
}

// Stores an enum as its short lower case name, never its number
func enumCol(e protoreflect.Enum) string { return eval.EnumShort(e) }

// Reads a short enum name back into its number, zero when unknown
func enumVal(d protoreflect.EnumDescriptor, short string) protoreflect.EnumNumber {
	prefix := screaming(string(d.Name())) + "_"
	want := strings.ToUpper(short)
	values := d.Values()
	for i := 0; i < values.Len(); i++ {
		v := values.Get(i)
		if strings.TrimPrefix(string(v.Name()), prefix) == want {
			return v.Number()
		}
	}
	return 0
}

// Converts CamelCase to SCREAMING_SNAKE the same way eval.EnumShort strips it
func screaming(s string) string {
	var b strings.Builder
	prev := rune(0)
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) && unicode.IsLower(prev) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToUpper(r))
		prev = r
	}
	return b.String()
}

func boolCol(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Writes map entries as rows with the given statement, in key order
func putMap(exec func(query string, args ...any) error, query, id string, m map[string]string) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		if err := exec(query, id, k, m[k]); err != nil {
			return err
		}
	}
	return nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
