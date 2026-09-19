package text

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Decodes JSON with numbers kept as json.Number
func DecodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	return root, nil
}

// Decodes JSON and flattens it into dotted string keys under prefix
func FlattenJSON(data []byte, prefix string, into map[string]string) error {
	root, err := DecodeJSON(data)
	if err != nil {
		return err
	}
	Flatten(prefix, root, into)
	return nil
}

// Flattens JSON to dotted keys. Scalar lists become comma-separated values with a length. Object
// lists use indexed keys.
func Flatten(prefix string, v any, into map[string]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			Flatten(key, child, into)
		}
	case []any:
		var parts []string
		for i, item := range t {
			switch item.(type) {
			case map[string]any, []any:
				Flatten(prefix+"."+strconv.Itoa(i), item, into)
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
