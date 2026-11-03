package jinja

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/transcode/vllm"
)

// The clock testdata/generate.py gives strftime_now
var fixedNow = time.Date(2026, 11, 2, 9, 5, 7, 123456000, time.Local)

func TestMain(m *testing.M) {
	now = func() time.Time { return fixedNow }
	os.Exit(m.Run())
}

// One request fixture rendered through the public API, naming its conversation and tool set
type goldenCase struct {
	Name                string                     `json:"name"`
	Template            string                     `json:"template"`
	ToolUse             string                     `json:"tool_use"`
	Conversation        string                     `json:"conversation"`
	Tools               string                     `json:"tools"`
	AddGenerationPrompt bool                       `json:"add_generation_prompt"`
	Kwargs              map[string]json.RawMessage `json:"kwargs"`
}

// A template rendered over a named variable set exactly as given, with no request shaping
type rawCase struct {
	Name     string `json:"name"`
	Template string `json:"template"`
	Fixture  string `json:"fixture"`
}

// Conversations, tool sets and raw variable sets the cases name
type fixtures struct {
	Conversations map[string][]vllm.Message  `json:"conversations"`
	Tools         map[string][]vllm.Tool     `json:"tools"`
	Raw           map[string]json.RawMessage `json:"raw"`
}

// What jinja2 produced, the output or the exception it raised with the line a syntax error carries
type goldenResult struct {
	Out     *string `json:"out"`
	Error   string  `json:"error"`
	Message string  `json:"message"`
	Lineno  int     `json:"lineno"`
}

// A testdata file's bytes, gunzipped when its name ends in .gz
func readTestdata(t testing.TB, parts ...string) []byte {
	t.Helper()
	path := filepath.Join(append([]string{"testdata"}, parts...)...)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, ".gz") {
		return b
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if b, err = io.ReadAll(zr); err != nil {
		t.Fatal(err)
	}
	return b
}

func readJSON(t testing.TB, v any, parts ...string) {
	t.Helper()
	if err := json.Unmarshal(readTestdata(t, parts...), v); err != nil {
		t.Fatal(err)
	}
}

// Fails unless the render matches jinja2's output or raised the same exception
func checkGolden(t *testing.T, want goldenResult, got string, err error) {
	t.Helper()
	if want.Out == nil {
		checkError(t, err, want)
		return
	}
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != *want.Out {
		t.Errorf("output differs\n--- got\n%q\n--- want\n%q", got, *want.Out)
	}
}

// Requires an error of the same exception class, matching its message for template raised errors
func checkError(t *testing.T, err error, want goldenResult) {
	t.Helper()
	if err == nil {
		t.Fatalf("rendered without error, want %s: %s", want.Error, want.Message)
	}
	var te *Error
	if !errors.As(err, &te) {
		t.Fatalf("error %v is not a template error, want %s: %s", err, want.Error, want.Message)
	}
	if want.Error == "TemplateError" && te.Msg != want.Message {
		t.Fatalf("message %q, want %q", te.Msg, want.Message)
	}
	if te.Kind != want.Error {
		t.Errorf("kind %s (%s), want %s (%s)", te.Kind, te.Msg, want.Error, want.Message)
	}
	if te.Line != want.Lineno {
		t.Errorf("%s at line %d, want line %d", te.Kind, te.Line, want.Lineno)
	}
}

// A testdata source and the module jinja2's code generator writes for it, or the exception it raises
type goldenSource struct {
	Src     string `json:"src"`
	Code    string `json:"code"`
	Error   string `json:"error"`
	Message string `json:"message"`
	Lineno  int    `json:"lineno"`
}

// Frames generate.py's source_records holds while Environment._parse runs, module, main, source_records, compile and _parse
const rawCompileBaseFrames = 5

// Generates every testdata source's module and compares it with jinja2's byte for byte, or the compile time exception
func TestGoldenSources(t *testing.T) {
	var sources []goldenSource
	readJSON(t, &sources, "golden", "sources.json.gz")
	if len(sources) == 0 {
		t.Fatal("no golden sources")
	}
	for i, s := range sources {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			py := &pyStack{depth: rawCompileBaseFrames, limit: recursionLimit}
			body, err := parseTemplate(s.Src, py)
			var g *codegen
			if err == nil {
				g, err = compileTree(body, py)
			}
			var te *Error
			if errors.As(err, &te) && (te.Kind == "SyntaxError" || te.Kind == "IndentationError") {
				err = nil
			}
			if s.Error != "" {
				checkExact(t, unitRow{Src: s.Src, Error: s.Error, Message: s.Message, Lineno: s.Lineno}, "", err)
				return
			}
			if err != nil {
				t.Fatalf("%q: %v", s.Src, err)
			}
			if got := g.source(); got != s.Code {
				t.Errorf("%q\n--- got\n%s\n--- want\n%s", s.Src, got, s.Code)
			}
		})
	}
}

// Renders every corpus template over every request fixture and compares with transformers byte for byte
func TestGoldenTemplates(t *testing.T) {
	var cases []goldenCase
	readJSON(t, &cases, "cases.json")
	var fx fixtures
	readJSON(t, &fx, "fixtures.json")
	var results map[string]goldenResult
	readJSON(t, &results, "golden", "cases.json.gz")
	if len(cases) != len(results) {
		t.Fatalf("%d cases, %d golden results", len(cases), len(results))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			want, ok := results[c.Name]
			if !ok {
				t.Fatal("no golden result")
			}
			src := &chattemplate.Template{Text: string(readTestdata(t, "templates", c.Template)), Source: c.Template}
			if c.ToolUse != "" {
				src.ToolUse = string(readTestdata(t, "templates", c.ToolUse))
			}
			tmpl, err := Compile(src)
			if err != nil {
				t.Fatal(err)
			}
			var r vllm.Renderer = tmpl
			if c.Kwargs != nil {
				kw := map[string]any{}
				for k, v := range c.Kwargs {
					kw[k] = v
				}
				r = tmpl.With(kw)
			}
			messages, ok := fx.Conversations[c.Conversation]
			if !ok {
				t.Fatalf("no conversation %q", c.Conversation)
			}
			got, err := r.Render(messages, fx.Tools[c.Tools], c.AddGenerationPrompt)
			checkGolden(t, want, got, err)
		})
	}
}

// Renders every corpus template over caller shaped variables and compares with transformers byte for byte
func TestGoldenRaw(t *testing.T) {
	var cases []rawCase
	readJSON(t, &cases, "raw.json")
	var fx fixtures
	readJSON(t, &fx, "fixtures.json")
	var results map[string]goldenResult
	readJSON(t, &results, "golden", "raw.json.gz")
	if len(cases) != len(results) {
		t.Fatalf("%d cases, %d golden results", len(cases), len(results))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			want, ok := results[c.Name]
			if !ok {
				t.Fatal("no golden result")
			}
			vars, ok := fx.Raw[c.Fixture]
			if !ok {
				t.Fatalf("no raw fixture %q", c.Fixture)
			}
			got, err := renderSource(string(readTestdata(t, "templates", c.Template)), vars)
			checkGolden(t, want, got, err)
		})
	}
}

type snippet struct {
	Name     string          `json:"name"`
	Template string          `json:"template"`
	Vars     json.RawMessage `json:"vars"`
}

// Renders handwritten snippets covering jinja2 semantics and compares with jinja2's output
func TestGoldenSnippets(t *testing.T) {
	var snippets []snippet
	readJSON(t, &snippets, "snippets.json")
	runSnippets(t, snippets, "snippets.json.gz")
}

// Renders randomly generated snippets over shared variables and compares with jinja2's output
func TestGoldenExprs(t *testing.T) {
	var exprs struct {
		Vars     json.RawMessage `json:"vars"`
		Snippets []snippet       `json:"snippets"`
	}
	readJSON(t, &exprs, "exprs.json.gz")
	for i := range exprs.Snippets {
		exprs.Snippets[i].Vars = exprs.Vars
	}
	runSnippets(t, exprs.Snippets, "exprs.json.gz")
}

func runSnippets(t *testing.T, snippets []snippet, file string) {
	var results map[string]goldenResult
	readJSON(t, &results, "golden", file)
	if len(snippets) != len(results) {
		t.Fatalf("%s holds %d snippets, %d golden results", file, len(snippets), len(results))
	}
	for _, s := range snippets {
		t.Run(s.Name, func(t *testing.T) {
			want, ok := results[s.Name]
			if !ok {
				t.Fatal("no golden result")
			}
			got, err := renderSource(s.Template, s.Vars)
			checkGolden(t, want, got, err)
		})
	}
}

// Parses source and renders it with the JSON object's keys as top level variables
func renderSource(source string, vars json.RawMessage) (string, error) {
	body, err := parse(source)
	if err != nil {
		return "", err
	}
	top := map[string]Value{}
	if len(vars) > 0 {
		v, err := jsonLoads(string(vars))
		if err != nil {
			return "", err
		}
		d := v.(*Dict)
		for i, k := range d.keys {
			top[k.(string)] = d.vals[i]
		}
	}
	return render(body, top, now)
}

// A random template with the deepest Python frames jinja2's parse and code generation phases reached compiling it
type goldenPeak struct {
	Src      string  `json:"src"`
	Out      *string `json:"out"`
	Error    string  `json:"error"`
	Message  string  `json:"message"`
	Lineno   int     `json:"lineno"`
	Parse    int     `json:"parse"`
	Generate int     `json:"generate"`
}

// Compiles random templates with an unbounded stack and requires the deepest frame each phase reached to equal Python's
func TestGoldenPeaks(t *testing.T) {
	var peaks []goldenPeak
	readJSON(t, &peaks, "golden", "peaks.json.gz")
	if len(peaks) == 0 {
		t.Fatal("no golden peaks")
	}
	for i, p := range peaks {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			py := newCompileStack()
			py.limit = 1 << 30
			body, err := parseTemplate(p.Src, py)
			if py.peak != p.Parse {
				t.Errorf("%q: parse peak %d, want %d", p.Src, py.peak, p.Parse)
			}
			if err == nil {
				gen := newCompileStack()
				gen.limit = 1 << 30
				_, err = compileTree(body, gen)
				if gen.peak != p.Generate {
					t.Errorf("%q: generate peak %d, want %d", p.Src, gen.peak, p.Generate)
				}
			}
			if p.Out != nil {
				if err != nil {
					t.Fatalf("%q: %v", p.Src, err)
				}
				return
			}
			checkExact(t, unitRow{Src: p.Src, Error: p.Error, Message: p.Message, Lineno: p.Lineno}, "", err)
		})
	}
}
