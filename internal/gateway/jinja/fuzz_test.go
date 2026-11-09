package jinja

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Variables a fuzzed template renders over, a tool calling conversation as chat templates receive it
const fuzzVars = `{"messages": [{"role": "system", "content": "You are helpful."}, {"role": "user", "content": "Weather in Paris?"},
 {"role": "assistant", "content": null, "tool_calls": [{"id": "call00001", "type": "function", "function": {"name": "get_weather", "arguments": {"city": "Paris"}}}]},
 {"role": "tool", "tool_call_id": "call00001", "content": "{\"temperature\": 22}"}, {"role": "assistant", "content": "It is 22 degrees."}],
 "tools": [{"type": "function", "function": {"name": "get_weather", "description": "Get weather", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}}],
 "add_generation_prompt": true, "bos_token": "<s>", "eos_token": "</s>", "documents": null}`

// Every template source the testdata tables hold, corpus templates, snippets, syntax and unit rows
func seedSources(f *testing.F) []string {
	paths, err := filepath.Glob(filepath.Join("testdata", "templates", "*.jinja"))
	if err != nil {
		f.Fatal(err)
	}
	var out []string
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		out = append(out, string(b))
	}
	var snippets []snippet
	readJSON(f, &snippets, "snippets.json")
	for _, s := range snippets {
		out = append(out, s.Template)
	}
	var syntax syntaxTables
	readJSON(f, &syntax, "syntax.json")
	for _, c := range syntax.Lex {
		out = append(out, c.Src)
	}
	for _, c := range syntax.Parse {
		out = append(out, c.Src)
	}
	for _, c := range syntax.Errors {
		out = append(out, c.Src)
	}
	var units unitTables
	readJSON(f, &units, "units.json")
	for _, table := range []map[string][]unitRow{units.Filters, units.Tests, units.Groups} {
		for _, rows := range table {
			for _, row := range rows {
				out = append(out, row.Src)
			}
		}
	}
	return out
}

// Parses any source without panicking, and renders whatever parses to output or a template error
func FuzzParse(f *testing.F) {
	for _, s := range seedSources(f) {
		f.Add(s)
	}
	v, err := jsonLoads(fuzzVars)
	if err != nil {
		f.Fatal(err)
	}
	vars := map[string]Value{}
	d := v.(*Dict)
	for i, k := range d.keys {
		vars[k.(string)] = d.vals[i]
	}
	f.Fuzz(func(t *testing.T, src string) {
		body, err := parse(src)
		var te *Error
		if err != nil {
			if !errors.As(err, &te) {
				t.Fatalf("parse failed with %T %v, not a template error", err, err)
			}
			return
		}
		detectOpenAIFormat(body)
		if _, err := render(body, vars, now); err != nil && !errors.As(err, &te) {
			t.Fatalf("render failed with %T %v, not a template error", err, err)
		}
	})
}
