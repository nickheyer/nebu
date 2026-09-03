package eval

import "testing"

func TestTemplateFuncs(t *testing.T) {
	cases := map[string]string{
		`{{join "," .list}}`:                   "a,b",
		`{{join "," .any}}`:                    "1,x",
		`{{lower .s}}-{{upper .s}}`:            "mixed-MIXED",
		`{{trimPrefix "card" .card}}`:          "7",
		`{{trimSuffix ".gguf" .file}}`:         "model",
		`{{replace "." "" .cc}}`:               "86",
		`{{if contains "x" .any1}}yes{{end}}`:  "yes",
		`{{if hasPrefix "ca" .card}}p{{end}}`:  "p",
		`{{if hasSuffix "guf" .file}}s{{end}}`: "s",
		`{{default "dflt" .empty}}`:            "dflt",
		`{{default "dflt" .s}}`:                "Mixed",
		`{{quote .s}}`:                         `"Mixed"`,
		`{{first .list}}`:                      "a",
		`{{num .cc}}`:                          "8.6",
		`{{range $i, $d := .devs}}{{if $i}},{{end}}{{index $d.facts "index"}}{{end}}`: "0,1",
		`{{trimSpace .padded}}`:       "tight",
		`{{len (split "," "a,b,c")}}`: "3",
	}
	ctx := map[string]any{
		"list":   []string{"a", "b"},
		"any":    []any{1, "x"},
		"any1":   "x",
		"s":      "Mixed",
		"card":   "card7",
		"file":   "model.gguf",
		"cc":     "8.6",
		"empty":  "",
		"padded": "  tight ",
		"devs":   []map[string]any{{"facts": map[string]any{"index": "0"}}, {"facts": map[string]any{"index": "1"}}},
	}
	for src, want := range cases {
		tpl, err := CompileTemplate(src)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		got, err := tpl.Render(ctx)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %q want %q", src, got, want)
		}
	}
}

func TestFirstAndJoinEmpty(t *testing.T) {
	if joinAny(",", nil) != "" || joinAny(",", 5) != "5" {
		t.Fatal("join edge cases")
	}
	if firstOf([]string{}) != nil || firstOf(3) != nil {
		t.Fatal("first edge cases")
	}
}
