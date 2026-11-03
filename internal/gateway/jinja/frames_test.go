package jinja

import (
	"encoding/json"
	"fmt"
	"testing"
)

// A grid template with the deepest frame its warmed jinja2 render reached above the root, and the outcome
type gridPeak struct {
	Src     string  `json:"src"`
	Peak    int     `json:"peak"`
	Out     *string `json:"out"`
	Error   string  `json:"error"`
	Message string  `json:"message"`
	Lineno  int     `json:"lineno"`
}

// Renders every value shape grid template and requires the deepest frame to equal warm Python's, and the same outcome
func TestGoldenFrameGrid(t *testing.T) {
	var grid []gridPeak
	readJSON(t, &grid, "golden", "framegrid.json.gz")
	if len(grid) == 0 {
		t.Fatal("no frame grid")
	}
	for i, g := range grid {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			body, err := parse(g.Src)
			if err != nil {
				t.Fatalf("%q: %v", g.Src, err)
			}
			_, peak, err := renderTracked(body, map[string]Value{}, now)
			if peak-rootFrames != g.Peak {
				t.Errorf("%q: peak %d frames above root, want %d", g.Src, peak-rootFrames, g.Peak)
			}
			if g.Out == nil {
				checkExact(t, unitRow{Src: g.Src, Error: g.Error, Message: g.Message, Lineno: g.Lineno}, "", err)
			} else if err != nil {
				t.Errorf("%q: %v", g.Src, err)
			}
		})
	}
}

// A measured cost of one key with the shape features of the value it was measured on
type warmCostPoint map[string]int

// Every measured key has its constant cost in the table, a shape rule reproducing each cost, or a model
func TestWarmFrameTable(t *testing.T) {
	var costs map[string][]warmCostPoint
	readJSON(t, &costs, "golden", "warmcosts.json.gz")
	if len(costs) == 0 || len(warmShapeRules) == 0 {
		t.Fatalf("%d measured keys, %d shape rules", len(costs), len(warmShapeRules))
	}
	for op := range warmModeled {
		if frameModels[op] == nil {
			t.Errorf("%s: modeled operation without a Go model", op)
		}
	}
	for op := range frameModels {
		if !warmModeled[op] {
			t.Errorf("%s: Go model for an operation generate.py does not mark modeled", op)
		}
	}
	for key, points := range costs {
		distinct := map[int]bool{}
		for _, p := range points {
			distinct[p["cost"]] = true
		}
		rule, shaped := warmShapeRules[key]
		cost, constant := warmFrames[key]
		modeled := modeledOp(key) != ""
		switch {
		case len(distinct) == 1 && (shaped || !constant || cost != points[0]["cost"]):
			t.Errorf("%s: measured constant %d, table %d %v, shape rule %v", key, points[0]["cost"], cost, constant, shaped)
		case len(distinct) > 1 && modeled:
			if shaped || constant {
				t.Errorf("%s: modeled key also in the measured tables", key)
			}
		case len(distinct) > 1 && (!shaped || constant):
			t.Errorf("%s: measured costs vary, shape rule %v, constant %v, modeled %v", key, shaped, constant, modeled)
		case len(distinct) > 1:
			if _, ok := shapeFeatures[rule.feature]; !ok {
				t.Errorf("%s: shape feature %q has no rule in the engine", key, rule.feature)
				continue
			}
			for _, p := range points {
				if got := max(rule.floor, rule.offset+rule.step*p[rule.feature]); got != p["cost"] {
					t.Errorf("%s: rule gives %d at %s %d, measured %d", key, got, rule.feature, p[rule.feature], p["cost"])
				}
			}
		}
	}
	for key := range warmShapeRules {
		if _, ok := costs[key]; !ok {
			t.Errorf("%s: shape rule for a key generate.py never measured", key)
		}
	}
}

// A source warm Python rendered with its variables and the most frames each measured operation key pushed
type warmRender struct {
	Src   string          `json:"src"`
	Vars  json.RawMessage `json:"vars"`
	Costs map[string]int  `json:"costs"`
}

// Renders every measured source and requires each operation key the engine prices to cost what warm Python measured
func TestWarmRenderCosts(t *testing.T) {
	var renders []warmRender
	readJSON(t, &renders, "golden", "warmrender.json.gz")
	if len(renders) == 0 {
		t.Fatal("no measured renders")
	}
	for i, w := range renders {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			costs, err := renderCosts(w.Src, w.Vars)
			switch {
			case w.Costs == nil && costs != nil:
				t.Fatalf("%q: compiles, jinja2 failed to compile it", w.Src)
			case w.Costs == nil:
				return
			case costs == nil:
				t.Fatalf("%q: %v", w.Src, err)
			}
			for key, got := range costs {
				want, ok := w.Costs[key]
				if !ok {
					t.Errorf("%q: %s priced at %d, never measured in this render", w.Src, key, got)
				} else if got != want {
					t.Errorf("%q: %s costs %d, warm Python %d", w.Src, key, got, want)
				}
			}
		})
	}
}

// Renders source with the JSON object's keys as variables, returning the most frames each operation key pushed
func renderCosts(source string, vars json.RawMessage) (map[string]int, error) {
	body, err := parse(source)
	if err != nil {
		return nil, err
	}
	top := map[string]Value{}
	v, err := jsonLoads(string(vars))
	if err != nil {
		return nil, err
	}
	d := v.(*Dict)
	for i, k := range d.keys {
		top[k.(string)] = d.vals[i]
	}
	r := &renderer{clock: now, frames: rootFrames, peak: rootFrames, costs: map[string]int{}}
	_, err = r.renderBody(body, top)
	return r.costs, err
}

// A grid value's expression with the frames each shape feature routine pushes on it in warm Python
type shapeFeatureRow map[string]any

// Every shape feature the engine computes on a grid value equals the frames warm Python measured for it
func TestShapeFeatures(t *testing.T) {
	var rows []shapeFeatureRow
	readJSON(t, &rows, "golden", "shapefeatures.json.gz")
	if len(rows) == 0 {
		t.Fatal("no shape feature rows")
	}
	for _, row := range rows {
		src := row["src"].(string)
		body, err := parse("{% set v = " + src + " %}")
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		r := &renderer{clock: now, frames: rootFrames, peak: rootFrames}
		globals := newScope(nil)
		for k, v := range r.globals() {
			globals.vars[k] = v
		}
		v, err := r.eval(body[0].(*setStmt).value, newScope(globals))
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		for name, feature := range shapeFeatures {
			want, ok := row[name].(float64)
			if !ok {
				t.Fatalf("%s: no measured %s feature", src, name)
			}
			if got := feature(v); got != int(want) {
				t.Errorf("%s: %s feature %d, want %d", src, name, got, int(want))
			}
		}
	}
}
