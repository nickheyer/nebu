// Package probes runs spec driven host probes and maps output onto profile entries.
package probes

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const defaultTimeout = 5 * time.Second

// One parsed record of probe output
type Row map[string]string

// Parsed output of one probe run
type Result struct {
	Rows   []Row
	Status v1.ProbeStatus
	Detail string
}

// Profile entries rendered from rows
type Emitted struct {
	Devices []*v1.Device
	Pools   []*v1.MemoryPool
	Facts   map[string]string
}

// Compiled probe ready to run
type Probe struct {
	Spec    *v1.ProbeSpec
	pattern *regexp.Regexp
	device  *deviceTemplate
	pool    *poolTemplate
	facts   []factTemplate
}

type deviceTemplate struct {
	kind   v1.DeviceKind
	fields map[string]*eval.Template
	facts  map[string]*eval.Template
	unit   string
	when   *eval.Expr
}

type poolTemplate struct {
	kind   v1.PoolKind
	fields map[string]*eval.Template
	unit   string
	when   *eval.Expr
}

type factTemplate struct {
	key   string
	value *eval.Template
}

// Compiles a probe spec, validating templates and patterns
func Compile(spec *v1.ProbeSpec) (*Probe, error) {
	p := &Probe{Spec: spec}
	if spec.GetExec() == nil || (spec.GetExec().GetCommand() == "" && spec.GetExec().GetFile() == "") {
		return nil, fmt.Errorf("probe %s: exec needs command or file", spec.GetId())
	}
	if spec.GetParse().GetKind() == v1.ParseKind_PARSE_KIND_REGEX {
		re, err := regexp.Compile(spec.GetParse().GetPattern())
		if err != nil {
			return nil, fmt.Errorf("probe %s: %w", spec.GetId(), err)
		}
		p.pattern = re
	}
	if spec.GetParse().GetKind() == v1.ParseKind_PARSE_KIND_UNSPECIFIED {
		return nil, fmt.Errorf("probe %s: parse kind required", spec.GetId())
	}
	emit := spec.GetEmit()
	if d := emit.GetDevice(); d != nil {
		if d.GetId() == "" {
			return nil, fmt.Errorf("probe %s: device id template required", spec.GetId())
		}
		fields, err := eval.CompileTemplates(nonEmpty(map[string]string{
			"id": d.GetId(), "name": d.GetName(), "vendor": d.GetVendor(),
			"memory_total": d.GetMemoryTotal(), "memory_free": d.GetMemoryFree(),
		}))
		if err != nil {
			return nil, fmt.Errorf("probe %s device: %w", spec.GetId(), err)
		}
		facts, err := eval.CompileTemplates(d.GetFacts())
		if err != nil {
			return nil, fmt.Errorf("probe %s device facts: %w", spec.GetId(), err)
		}
		when, err := compileWhen(d.GetWhen())
		if err != nil {
			return nil, fmt.Errorf("probe %s device: %w", spec.GetId(), err)
		}
		p.device = &deviceTemplate{kind: d.GetKind(), fields: fields, facts: facts, unit: d.GetUnit(), when: when}
	}
	if pl := emit.GetPool(); pl != nil {
		if pl.GetId() == "" {
			return nil, fmt.Errorf("probe %s: pool id template required", spec.GetId())
		}
		fields, err := eval.CompileTemplates(nonEmpty(map[string]string{
			"id": pl.GetId(), "device_id": pl.GetDeviceId(), "total": pl.GetTotal(), "free": pl.GetFree(),
		}))
		if err != nil {
			return nil, fmt.Errorf("probe %s pool: %w", spec.GetId(), err)
		}
		when, err := compileWhen(pl.GetWhen())
		if err != nil {
			return nil, fmt.Errorf("probe %s pool: %w", spec.GetId(), err)
		}
		p.pool = &poolTemplate{kind: pl.GetKind(), fields: fields, unit: pl.GetUnit(), when: when}
	}
	for _, f := range emit.GetFacts() {
		t, err := eval.CompileTemplate(f.GetValue())
		if err != nil {
			return nil, fmt.Errorf("probe %s fact %s: %w", spec.GetId(), f.GetKey(), err)
		}
		p.facts = append(p.facts, factTemplate{key: f.GetKey(), value: t})
	}
	return p, nil
}

// Runs exec and parse, never returning a Go error for host conditions
func (p *Probe) Run(ctx context.Context) Result {
	data, status, detail := p.execute(ctx)
	if status != v1.ProbeStatus_PROBE_STATUS_OK {
		return Result{Status: status, Detail: detail}
	}
	rows, err := p.parse(data)
	if err != nil {
		return Result{Status: v1.ProbeStatus_PROBE_STATUS_FAILED, Detail: err.Error()}
	}
	return Result{Rows: rows, Status: v1.ProbeStatus_PROBE_STATUS_OK, Detail: fmt.Sprintf("%d rows", len(rows))}
}

// Renders devices, pools, and facts from rows
func (p *Probe) Emit(rows []Row) (*Emitted, error) {
	out := &Emitted{Facts: map[string]string{}}
	for _, row := range rows {
		env := rowEnv(row)
		if p.device != nil {
			ok, err := whenOK(p.device.when, env)
			if err != nil {
				return nil, err
			}
			if ok {
				d, err := p.renderDevice(row)
				if err != nil {
					return nil, err
				}
				out.Devices = append(out.Devices, d)
			}
		}
		if p.pool != nil {
			ok, err := whenOK(p.pool.when, env)
			if err != nil {
				return nil, err
			}
			if ok {
				pl, err := p.renderPool(row)
				if err != nil {
					return nil, err
				}
				out.Pools = append(out.Pools, pl)
			}
		}
	}
	if len(p.facts) > 0 {
		ctx := map[string]any{"rows": rows, "first": Row{}, "count": len(rows)}
		if len(rows) > 0 {
			ctx["first"] = rows[0]
		}
		for _, f := range p.facts {
			v, err := f.value.Render(ctx)
			if err != nil {
				return nil, err
			}
			out.Facts[f.key] = strings.TrimSpace(v)
		}
	}
	return out, nil
}

func (p *Probe) renderDevice(row Row) (*v1.Device, error) {
	fields, err := eval.RenderTemplates(p.device.fields, row)
	if err != nil {
		return nil, err
	}
	facts, err := eval.RenderTemplates(p.device.facts, row)
	if err != nil {
		return nil, err
	}
	d := &v1.Device{
		Id:     strings.TrimSpace(fields["id"]),
		Kind:   p.device.kind,
		Vendor: strings.TrimSpace(fields["vendor"]),
		Name:   strings.TrimSpace(fields["name"]),
		Facts:  trimAll(facts),
	}
	if d.MemoryTotalBytes, err = optionalBytes(fields["memory_total"], p.device.unit); err != nil {
		return nil, err
	}
	if d.MemoryFreeBytes, err = optionalBytes(fields["memory_free"], p.device.unit); err != nil {
		return nil, err
	}
	return d, nil
}

func (p *Probe) renderPool(row Row) (*v1.MemoryPool, error) {
	fields, err := eval.RenderTemplates(p.pool.fields, row)
	if err != nil {
		return nil, err
	}
	pl := &v1.MemoryPool{
		Id:       strings.TrimSpace(fields["id"]),
		Kind:     p.pool.kind,
		DeviceId: strings.TrimSpace(fields["device_id"]),
	}
	if pl.TotalBytes, err = optionalBytes(fields["total"], p.pool.unit); err != nil {
		return nil, err
	}
	if pl.FreeBytes, err = optionalBytes(fields["free"], p.pool.unit); err != nil {
		return nil, err
	}
	return pl, nil
}

func (p *Probe) execute(ctx context.Context) ([]byte, v1.ProbeStatus, string) {
	ex := p.Spec.GetExec()
	if ex.GetFile() != "" {
		data, err := os.ReadFile(ex.GetFile())
		if err != nil {
			return nil, v1.ProbeStatus_PROBE_STATUS_SKIPPED, err.Error()
		}
		return data, v1.ProbeStatus_PROBE_STATUS_OK, ""
	}
	path, err := exec.LookPath(ex.GetCommand())
	if err != nil {
		return nil, v1.ProbeStatus_PROBE_STATUS_SKIPPED, fmt.Sprintf("%s not found", ex.GetCommand())
	}
	timeout := defaultTimeout
	if ex.GetTimeoutMs() > 0 {
		timeout = time.Duration(ex.GetTimeoutMs()) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, ex.GetArgs()...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, v1.ProbeStatus_PROBE_STATUS_FAILED, detail
	}
	return stdout.Bytes(), v1.ProbeStatus_PROBE_STATUS_OK, ""
}

func (p *Probe) parse(data []byte) ([]Row, error) {
	ps := p.Spec.GetParse()
	switch ps.GetKind() {
	case v1.ParseKind_PARSE_KIND_CSV:
		return parseCSV(data, ps.GetColumns())
	case v1.ParseKind_PARSE_KIND_JSON:
		return parseJSON(data, ps.GetRows())
	case v1.ParseKind_PARSE_KIND_KV:
		return parseKV(data, ps.GetBlockSeparator()), nil
	case v1.ParseKind_PARSE_KIND_REGEX:
		return parseRegex(data, p.pattern), nil
	}
	return nil, fmt.Errorf("unsupported parse kind %s", ps.GetKind())
}

func parseCSV(data []byte, columns []string) ([]Row, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var rows []Row
	for _, rec := range records {
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue
		}
		row := Row{}
		for j, col := range columns {
			if j < len(rec) {
				row[Key(col)] = strings.TrimSpace(rec[j])
			} else {
				row[Key(col)] = ""
			}
		}
		rows = append(rows, withIndex(row, len(rows)))
	}
	return rows, nil
}

func parseJSON(data []byte, path string) ([]Row, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	node := root
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			continue
		}
		m, ok := node.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("json path %q: not an object at %q", path, seg)
		}
		node = m[seg]
	}
	var rows []Row
	switch n := node.(type) {
	case []any:
		for _, item := range n {
			if m, ok := item.(map[string]any); ok {
				rows = append(rows, withIndex(flatten("", m, Row{}), len(rows)))
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if m, ok := n[k].(map[string]any); ok {
				row := flatten("", m, Row{})
				row["key"] = k
				rows = append(rows, withIndex(row, len(rows)))
			}
		}
	default:
		return nil, fmt.Errorf("json path %q: not rows", path)
	}
	return rows, nil
}

func parseKV(data []byte, sep string) []Row {
	if sep == "" {
		sep = "\n\n"
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	var rows []Row
	for _, block := range strings.Split(text, sep) {
		row := Row{}
		for _, line := range strings.Split(block, "\n") {
			k, v, ok := splitKV(line)
			if ok {
				row[Key(k)] = v
			}
		}
		if len(row) > 0 {
			rows = append(rows, withIndex(row, len(rows)))
		}
	}
	return rows
}

func parseRegex(data []byte, re *regexp.Regexp) []Row {
	var rows []Row
	names := re.SubexpNames()
	for _, m := range re.FindAllStringSubmatch(string(data), -1) {
		row := Row{}
		for i, name := range names {
			if name != "" && i < len(m) {
				row[Key(name)] = strings.TrimSpace(m[i])
			}
		}
		rows = append(rows, withIndex(row, len(rows)))
	}
	return rows
}

func splitKV(line string) (string, string, bool) {
	for _, sep := range []string{":", "="} {
		if i := strings.Index(line, sep); i > 0 {
			return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
		}
	}
	return "", "", false
}

func flatten(prefix string, m map[string]any, into Row) Row {
	eval.Flatten(prefix, m, into, Key)
	return into
}

func withIndex(row Row, i int) Row {
	if _, ok := row["index"]; !ok {
		row["index"] = strconv.Itoa(i)
	}
	return row
}

var keyClean = regexp.MustCompile(`[^A-Za-z0-9]+`)

// Normalizes a field name to identifier characters
func Key(s string) string {
	return strings.Trim(keyClean.ReplaceAllString(strings.TrimSpace(s), "_"), "_")
}

// Parses a byte count with an optional inline or default unit
func Bytes(s, unit string) (uint64, error) { return eval.Bytes(s, unit) }

func optionalBytes(s, unit string) (uint64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return Bytes(s, unit)
}

func compileWhen(src string) (*eval.Expr, error) {
	if strings.TrimSpace(src) == "" {
		return nil, nil
	}
	return eval.Compile(src)
}

func whenOK(e *eval.Expr, env map[string]any) (bool, error) {
	if e == nil {
		return true, nil
	}
	return e.Bool(env)
}

func rowEnv(row Row) map[string]any {
	env := make(map[string]any, len(row))
	for k, v := range row {
		env[k] = v
	}
	return env
}

func nonEmpty(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

func trimAll(m map[string]string) map[string]string {
	for k, v := range m {
		m[k] = strings.TrimSpace(v)
	}
	return m
}
