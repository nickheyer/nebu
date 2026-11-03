package requestcontrol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync"

	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

// The registry type of the reference guardrail plugin
const GuardrailType = "guardrail"

// Answered 403 by default, a guardrail refused the request or the answer
var ErrGuardrailBlocked = errors.New("blocked by guardrail")

// What a guardrail decided about a request or a chunk of its answer
type Verdict int

const (
	VerdictAllow Verdict = iota
	VerdictBlock
	VerdictRedact
)

// Judges requests before scheduling and answer chunks on the way back, blocking or redacting either
type Guardrail interface {
	// The verdict on a request and, for VerdictRedact, the redacted body
	CheckRequest(ctx context.Context, in *Inbound, req *framework.Request) (Verdict, bodyparse.Document, error)
	// The verdict on a run of answer bytes and, for VerdictRedact, the redacted bytes
	CheckChunk(ctx context.Context, dec *Decision, chunk []byte, end bool) (Verdict, []byte, error)
}

// A guardrail refusal with the status it answers, 403 unless the guardrail chose 400
type BlockedError struct {
	Status int
	Reason string
}

func (e *BlockedError) Error() string { return "blocked by guardrail: " + e.Reason }

func (e *BlockedError) Unwrap() error { return ErrGuardrailBlocked }

// Runs a guardrail as a request control plugin, holding streamed bytes back to a line boundary so no match splits
type GuardrailPlugin struct {
	name string
	rail Guardrail
	held sync.Map
}

// Wraps a guardrail under a plugin name
func WrapGuardrail(name string, rail Guardrail) *GuardrailPlugin {
	return &GuardrailPlugin{name: name, rail: rail}
}

func (p *GuardrailPlugin) TypedName() framework.TypedName {
	return framework.TypedName{Type: GuardrailType, Name: p.name}
}

func (p *GuardrailPlugin) PreRequestBody(ctx context.Context, in *Inbound, req *framework.Request) (bodyparse.Document, error) {
	verdict, doc, err := p.rail.CheckRequest(ctx, in, req)
	switch {
	case err != nil:
		return nil, err
	case verdict == VerdictBlock:
		return nil, &BlockedError{Status: http.StatusForbidden, Reason: "the request matched a guardrail"}
	case verdict == VerdictRedact:
		return doc, nil
	}
	return nil, nil
}

// Judges the complete lines of a stream and the whole of an unstreamed answer, holding the rest for the next chunk
func (p *GuardrailPlugin) ResponseBody(ctx context.Context, dec *Decision, resp *framework.Response, chunk []byte, end bool) ([]byte, error) {
	var held []byte
	if v, ok := p.held.LoadAndDelete(dec); ok {
		held = v.([]byte)
	}
	buf := append(held, chunk...)
	ready, rest := buf, []byte(nil)
	switch {
	case end:
	case dec.Request == nil || !dec.Request.Streaming:
		ready, rest = nil, buf
	default:
		cut := bytes.LastIndexByte(buf, '\n')
		if cut < 0 {
			ready, rest = nil, buf
		} else {
			ready, rest = buf[:cut+1], buf[cut+1:]
		}
	}
	if len(rest) > 0 {
		p.held.Store(dec, rest)
	}
	if len(ready) == 0 && !end {
		return nil, nil
	}
	verdict, out, err := p.rail.CheckChunk(ctx, dec, ready, end)
	switch {
	case err != nil:
		return nil, err
	case verdict == VerdictBlock:
		p.held.Delete(dec)
		return nil, &BlockedError{Status: http.StatusForbidden, Reason: "the answer matched a guardrail"}
	case verdict == VerdictRedact:
		return out, nil
	}
	return ready, nil
}

func (p *GuardrailPlugin) ResponseComplete(_ context.Context, dec *Decision, _ *framework.Response) {
	p.held.Delete(dec)
}

// Parameters of the reference guardrail, deny patterns that block and the PII classes that are redacted
type PatternParams struct {
	// Regular expressions that block a request or answer when they match its text
	Deny []string `json:"deny,omitempty"`
	// Whether email addresses, phone numbers, social security and card numbers are replaced by a marker
	PII bool `json:"pii,omitempty"`
	// What redacted text is replaced with
	Marker string `json:"marker,omitempty"`
	// The status a blocked request answers, 403 or 400
	Status int `json:"status,omitempty"`
}

// The marker redacted text is replaced with unless the parameters name one
const DefaultMarker = "[REDACTED]"

// Blocks on deny patterns and redacts personal data inside JSON strings, the reference guardrail
type PatternGuardrail struct {
	deny   []*regexp.Regexp
	pii    []*regexp.Regexp
	marker []byte
	status int
}

// Personal data classes the reference guardrail redacts
var piiPatterns = []*regexp.Regexp{
	regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`),
	regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
	regexp.MustCompile(`\b(?:\d[ -]?){13,16}\b`),
	regexp.MustCompile(`(?:\+\d{1,3}[ .-]?)?\(?\d{3}\)?[ .-]?\d{3}[ .-]?\d{4}\b`),
}

// Builds the reference guardrail from its parameters
func NewPatternGuardrail(params PatternParams) (*PatternGuardrail, error) {
	g := &PatternGuardrail{marker: []byte(DefaultMarker), status: http.StatusForbidden}
	for _, src := range params.Deny {
		re, err := regexp.Compile(src)
		if err != nil {
			return nil, fmt.Errorf("deny pattern %q: %w", src, err)
		}
		g.deny = append(g.deny, re)
	}
	if params.PII {
		g.pii = piiPatterns
	}
	if params.Marker != "" {
		if bytes.ContainsAny([]byte(params.Marker), "\"\\") {
			return nil, fmt.Errorf("marker %q may not hold a quote or a backslash", params.Marker)
		}
		g.marker = []byte(params.Marker)
	}
	switch params.Status {
	case 0, http.StatusForbidden:
	case http.StatusBadRequest:
		g.status = params.Status
	default:
		return nil, fmt.Errorf("status %d is neither 403 nor 400", params.Status)
	}
	if len(g.deny) == 0 && len(g.pii) == 0 {
		return nil, errors.New("a guardrail needs a deny pattern or pii redaction")
	}
	return g, nil
}

// Builds the reference guardrail plugin from chain parameters
func NewGuardrailPlugin(name string, params map[string]any) (framework.Plugin, error) {
	var pp PatternParams
	if err := DecodeParams(params, &pp); err != nil {
		return nil, err
	}
	g, err := NewPatternGuardrail(pp)
	if err != nil {
		return nil, err
	}
	return WrapGuardrail(name, g), nil
}

// Whether any deny pattern matches the text
func (g *PatternGuardrail) denied(text []byte) bool {
	for _, re := range g.deny {
		if re.Match(text) {
			return true
		}
	}
	return false
}

func (g *PatternGuardrail) CheckRequest(_ context.Context, in *Inbound, _ *framework.Request) (Verdict, bodyparse.Document, error) {
	if in.Body == nil {
		return VerdictAllow, nil, nil
	}
	if g.denied([]byte(in.Body.PromptText())) {
		return VerdictBlock, nil, &BlockedError{Status: g.status, Reason: "the prompt matched a deny pattern"}
	}
	if len(g.pii) == 0 {
		return VerdictAllow, nil, nil
	}
	body := in.Body.Bytes()
	out := g.redactStrings(body)
	if bytes.Equal(out, body) {
		return VerdictAllow, nil, nil
	}
	doc, err := bodyparse.Parse(in.Body.Kind(), out)
	if err != nil {
		return VerdictBlock, nil, err
	}
	return VerdictRedact, doc, nil
}

func (g *PatternGuardrail) CheckChunk(_ context.Context, _ *Decision, chunk []byte, _ bool) (Verdict, []byte, error) {
	if g.denied(chunk) {
		return VerdictBlock, nil, &BlockedError{Status: g.status, Reason: "the answer matched a deny pattern"}
	}
	if len(g.pii) == 0 {
		return VerdictAllow, chunk, nil
	}
	out := g.redactStrings(chunk)
	if bytes.Equal(out, chunk) {
		return VerdictAllow, chunk, nil
	}
	return VerdictRedact, out, nil
}

// Replaces personal data inside every JSON string literal, or in the whole text when it holds none
func (g *PatternGuardrail) redactStrings(b []byte) []byte {
	if bytes.IndexByte(b, '"') < 0 {
		return g.redact(b)
	}
	var out []byte
	last := 0
	for i := 0; i < len(b); i++ {
		if b[i] != '"' {
			continue
		}
		end := closingQuote(b, i)
		if end < 0 {
			break
		}
		inner := b[i+1 : end]
		if red := g.redact(inner); !bytes.Equal(red, inner) {
			out = append(out, b[last:i+1]...)
			out = append(out, red...)
			last = end
		}
		i = end
	}
	if out == nil {
		return b
	}
	return append(out, b[last:]...)
}

// The index of the quote closing the string literal opening at b[i], negative when it never closes
func closingQuote(b []byte, i int) int {
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++
		case '"':
			return j
		}
	}
	return -1
}

// Replaces every personal data match with the marker
func (g *PatternGuardrail) redact(b []byte) []byte {
	out := b
	for _, re := range g.pii {
		out = re.ReplaceAll(out, g.marker)
	}
	return out
}
