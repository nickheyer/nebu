package bodyparse

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync"
	"unicode/utf8"
)

// One top level member, the key bounds excluding its quotes and esc marking keys that need decoding
type field struct {
	ks, ke int
	vs, ve int
	esc    bool
}

type document struct {
	body   []byte
	kind   Kind
	open   int
	fields []field
	inline [16]field

	modelOnce sync.Once
	model     string
	modelErr  error

	promptOnce sync.Once
	prompt     string

	tokensOnce sync.Once
	tokens     []int

	scalarOnce sync.Once
	stream     bool
	maxTokens  int

	viewMu sync.Mutex
	views  map[ViewKey]*view
}

// One derived value, built once under its key
type view struct {
	once sync.Once
	v    any
	err  error
}

// Validates the body as one JSON object and records every top level member in a single pass
func Parse(kind Kind, body []byte) (Document, error) {
	d := &document{body: body, kind: kind}
	d.fields = d.inline[:0]
	n := len(body)
	i := skipWS(body, 0)
	if i >= n || body[i] != '{' {
		return nil, fmt.Errorf("%w: no object at offset %d", ErrNotJSON, i)
	}
	d.open = i
	i = skipWS(body, i+1)
	if i < n && body[i] == '}' {
		i++
	} else {
		for {
			if i >= n || body[i] != '"' {
				return nil, fmt.Errorf("%w: expected a key at offset %d", ErrNotJSON, i)
			}
			ke, esc, ok := scanString(body, i)
			if !ok {
				return nil, fmt.Errorf("%w: bad key at offset %d", ErrNotJSON, ke)
			}
			f := field{ks: i + 1, ke: ke - 1, esc: needsDecode(body[i+1:ke-1], esc)}
			i = skipWS(body, ke)
			if i >= n || body[i] != ':' {
				return nil, fmt.Errorf("%w: expected a colon at offset %d", ErrNotJSON, i)
			}
			f.vs = skipWS(body, i+1)
			if f.ve, ok = scanValue(body, f.vs, 1); !ok {
				return nil, fmt.Errorf("%w: bad value at offset %d", ErrNotJSON, f.ve)
			}
			d.fields = append(d.fields, f)
			i = skipWS(body, f.ve)
			if i < n && body[i] == ',' {
				i = skipWS(body, i+1)
				continue
			}
			if i < n && body[i] == '}' {
				i++
				break
			}
			return nil, fmt.Errorf("%w: expected a comma or brace at offset %d", ErrNotJSON, i)
		}
	}
	if i = skipWS(body, i); i != n {
		return nil, fmt.Errorf("%w: trailing bytes at offset %d", ErrNotJSON, i)
	}
	return d, nil
}

func (d *document) Bytes() []byte { return d.body }
func (d *document) Kind() Kind    { return d.kind }

// The last member named name, as encoding/json keeps the last of duplicate keys
func (d *document) field(name string) (field, bool) {
	for i := len(d.fields) - 1; i >= 0; i-- {
		f := d.fields[i]
		if keyIs(d.body[f.ks:f.ke], f.esc, name) {
			return f, true
		}
	}
	return field{}, false
}

// The raw value of the last member named name
func (d *document) value(name string) ([]byte, bool) {
	f, ok := d.field(name)
	if !ok {
		return nil, false
	}
	return d.body[f.vs:f.ve], true
}

func (d *document) Field(name string) ([]byte, bool) {
	return d.value(name)
}

func (d *document) Model() (string, error) {
	d.modelOnce.Do(func() {
		v, ok := d.value("model")
		switch {
		case !ok:
			d.modelErr = ErrNoModel
		case v[0] != '"':
			d.modelErr = fmt.Errorf("%w: the model field is not a string", ErrNoModel)
		default:
			if d.model = unquote(v); d.model == "" {
				d.modelErr = fmt.Errorf("%w: the model field is empty", ErrNoModel)
			}
		}
	})
	return d.model, d.modelErr
}

func (d *document) PromptText() string {
	d.promptOnce.Do(func() { d.prompt = d.promptText() })
	return d.prompt
}

func (d *document) PromptTokenIDs() []int {
	d.tokensOnce.Do(func() {
		switch d.kind {
		case KindCompletions:
			d.tokens = d.tokenIDs("prompt")
		case KindEmbeddings:
			d.tokens = d.tokenIDs("input")
		case KindUnknown:
			if d.tokens = d.tokenIDs("prompt"); d.tokens == nil {
				d.tokens = d.tokenIDs("input")
			}
		}
	})
	return d.tokens
}

func (d *document) Stream() bool {
	d.scalars()
	return d.stream
}

func (d *document) MaxTokens() int {
	d.scalars()
	return d.maxTokens
}

// Reads the stream flag and the output bound named by the kind
func (d *document) scalars() {
	d.scalarOnce.Do(func() {
		if v, ok := d.value("stream"); ok {
			d.stream = string(v) == "true"
		}
		var names []string
		switch d.kind {
		case KindChatCompletions:
			names = []string{"max_completion_tokens", "max_tokens"}
		case KindCompletions, KindAnthropicMessages:
			names = []string{"max_tokens"}
		case KindResponses:
			names = []string{"max_output_tokens"}
		case KindOllamaChat, KindOllamaGenerate:
			d.maxTokens = d.numPredict()
			return
		case KindUnknown:
			names = []string{"max_completion_tokens", "max_tokens", "max_output_tokens"}
		}
		for _, name := range names {
			if v, ok := d.value(name); ok && v[0] != 'n' {
				d.maxTokens = count(v)
				return
			}
		}
		if d.kind == KindUnknown {
			d.maxTokens = d.numPredict()
		}
	})
}

// Ollama's options.num_predict, negative values meaning unbounded
func (d *document) numPredict() int {
	v, ok := d.value("options")
	if !ok || v[0] != '{' {
		return 0
	}
	it := objectAt(v, 0)
	for {
		key, esc, vs, ve, ok := it.next()
		if !ok {
			return 0
		}
		if keyIs(key, esc, "num_predict") {
			return count(v[vs:ve])
		}
	}
}

// A whole count from a raw validated number, overflow saturating and anything not positive reading zero
func count(v []byte) int {
	if v[0] != '-' && !isDigit(v[0]) {
		return 0
	}
	f, _ := strconv.ParseFloat(string(v), 64)
	if !(f > 0) {
		return 0
	}
	return int(min(f, math.MaxInt32))
}

// Token ids from an integer array or an array of integer arrays, nil when the value holds text
func (d *document) tokenIDs(name string) []int {
	v, ok := d.value(name)
	if !ok || v[0] != '[' {
		return nil
	}
	var out []int
	it := arrayAt(v, 0)
	for {
		vs, ve, ok := it.next()
		if !ok {
			return out
		}
		if v[vs] != '[' {
			id, ok := tokenID(v[vs:ve])
			if !ok {
				return nil
			}
			out = append(out, id)
			continue
		}
		inner := arrayAt(v, vs)
		for {
			is, ie, ok := inner.next()
			if !ok {
				break
			}
			id, ok := tokenID(v[is:ie])
			if !ok {
				return nil
			}
			out = append(out, id)
		}
	}
}

func tokenID(v []byte) (int, bool) {
	if !isDigit(v[0]) {
		return 0, false
	}
	id, err := strconv.Atoi(string(v))
	return id, err == nil
}

func (d *document) WithModel(model string) (Document, error) {
	if model == "" {
		return nil, ErrNoModel
	}
	return d.derive(d.set(map[string][]byte{"model": appendQuoted(nil, model)}))
}

func (d *document) WithField(name string, rawJSON []byte) (Document, error) {
	return d.WithFields(map[string][]byte{name: rawJSON})
}

func (d *document) WithFields(fields map[string][]byte) (Document, error) {
	for name, raw := range fields {
		if !utf8.ValidString(name) {
			return nil, fmt.Errorf("field name %q is not valid UTF-8", name)
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("%w: the value for field %q", ErrNotJSON, name)
		}
	}
	if len(fields) == 0 {
		return d.derive(d.body)
	}
	return d.derive(d.set(fields))
}

// A fresh Document of the same kind over mutated bytes, its views dropped since they came from the old bytes
func (d *document) derive(body []byte) (Document, error) {
	return Parse(d.kind, body)
}

// One member to rewrite, its bounds and the value it takes
type edit struct {
	vs, ve int
	val    []byte
}

// Replaces the value of every member named in fields in one pass, appending the absent names after the last member
func (d *document) set(fields map[string][]byte) []byte {
	var edits []edit
	present := make(map[string]bool, len(fields))
	grow := 0
	for _, f := range d.fields {
		for name, val := range fields {
			if keyIs(d.body[f.ks:f.ke], f.esc, name) {
				edits = append(edits, edit{vs: f.vs, ve: f.ve, val: val})
				present[name] = true
				grow += len(val) - (f.ve - f.vs)
				break
			}
		}
	}
	var absent []string
	for name := range fields {
		if !present[name] {
			absent = append(absent, name)
			grow += len(name) + len(fields[name]) + 4
		}
	}
	slices.Sort(absent)
	slices.SortFunc(edits, func(a, b edit) int { return a.vs - b.vs })
	out := make([]byte, 0, len(d.body)+max(grow, 0))
	last := 0
	for _, e := range edits {
		out = append(out, d.body[last:e.vs]...)
		out = append(out, e.val...)
		last = e.ve
	}
	if len(absent) == 0 {
		return append(out, d.body[last:]...)
	}
	at, sep := d.open+1, ""
	if len(d.fields) > 0 {
		at, sep = d.fields[len(d.fields)-1].ve, ","
	}
	out = append(out, d.body[last:at]...)
	for _, name := range absent {
		out = append(out, sep...)
		out = appendQuoted(out, name)
		out = append(out, ':')
		out = append(out, fields[name]...)
		sep = ","
	}
	return append(out, d.body[at:]...)
}

func (d *document) View(key ViewKey, build func(Document) (any, error)) (any, error) {
	d.viewMu.Lock()
	v, ok := d.views[key]
	if !ok {
		if d.views == nil {
			d.views = map[ViewKey]*view{}
		}
		v = &view{}
		d.views[key] = v
	}
	d.viewMu.Unlock()
	v.once.Do(func() { v.v, v.err = build(d) })
	return v.v, v.err
}

func (d *document) WithView(key ViewKey, value any) Document {
	out := &document{body: d.body, kind: d.kind, open: d.open, fields: d.fields, views: map[ViewKey]*view{}}
	d.viewMu.Lock()
	for k, v := range d.views {
		out.views[k] = v
	}
	d.viewMu.Unlock()
	set := &view{v: value}
	set.once.Do(func() {})
	out.views[key] = set
	return out
}
