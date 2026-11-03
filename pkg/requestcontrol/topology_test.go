package requestcontrol

import (
	"errors"
	"slices"
	"testing"

	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

func names(batches [][]framework.Plugin) [][]string {
	var out [][]string
	for _, b := range batches {
		var level []string
		for _, p := range b {
			level = append(level, p.TypedName().Name)
		}
		out = append(out, level)
	}
	return out
}

func pre(name string) *prePlugin   { return &prePlugin{name: name} }
func post(name string) *postPlugin { return &postPlugin{name: name} }

func TestTopologyBatches(t *testing.T) {
	a, b, c, d := pre("a"), pre("b"), pre("c"), pre("d")
	x := post("x")
	both := &bothPlugin{name: "both"}
	cases := []struct {
		name  string
		regs  []Registration
		stage Stage
		want  [][]string
	}{
		{"independent run together", []Registration{{Plugin: a}, {Plugin: b}, {Plugin: c}}, StagePreRequest, [][]string{{"a", "b", "c"}}},
		{"chain", []Registration{{Plugin: c, After: []framework.TypedName{b.TypedName()}}, {Plugin: b, After: []framework.TypedName{a.TypedName()}}, {Plugin: a}}, StagePreRequest, [][]string{{"a"}, {"b"}, {"c"}}},
		{"diamond", []Registration{
			{Plugin: a, Before: []framework.TypedName{b.TypedName(), c.TypedName()}},
			{Plugin: b}, {Plugin: c},
			{Plugin: d, After: []framework.TypedName{b.TypedName(), c.TypedName()}},
		}, StagePreRequest, [][]string{{"a"}, {"b", "c"}, {"d"}}},
		{"long path sets the level", []Registration{
			{Plugin: a}, {Plugin: b, After: []framework.TypedName{a.TypedName()}},
			{Plugin: c, After: []framework.TypedName{a.TypedName(), b.TypedName()}},
		}, StagePreRequest, [][]string{{"a"}, {"b"}, {"c"}}},
		{"order through another stage", []Registration{
			{Plugin: a, Before: []framework.TypedName{x.TypedName()}},
			{Plugin: x, Before: []framework.TypedName{b.TypedName()}},
			{Plugin: b},
		}, StagePreRequest, [][]string{{"a"}, {"b"}}},
		{"stage filters members", []Registration{{Plugin: a}, {Plugin: x}, {Plugin: both, After: []framework.TypedName{x.TypedName()}}}, StagePostDecision, [][]string{{"x"}, {"both"}}},
		{"stage without plugins", []Registration{{Plugin: a}}, StageResponseBody, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			topo, err := NewTopology(c.regs...)
			if err != nil {
				t.Fatal(err)
			}
			got := names(topo.Batches(c.stage))
			if !slices.EqualFunc(got, c.want, slices.Equal) {
				t.Fatalf("batches %v, want %v", got, c.want)
			}
		})
	}
}

func TestTopologyRejects(t *testing.T) {
	a, b, c := pre("a"), pre("b"), pre("c")
	x := post("x")
	cases := []struct {
		name  string
		regs  []Registration
		cycle bool
	}{
		{"two cycle", []Registration{{Plugin: a, After: []framework.TypedName{b.TypedName()}}, {Plugin: b, After: []framework.TypedName{a.TypedName()}}}, true},
		{"three cycle", []Registration{
			{Plugin: a, Before: []framework.TypedName{b.TypedName()}},
			{Plugin: b, Before: []framework.TypedName{c.TypedName()}},
			{Plugin: c, Before: []framework.TypedName{a.TypedName()}},
		}, true},
		{"self", []Registration{{Plugin: a, After: []framework.TypedName{a.TypedName()}}}, true},
		{"cycle through another stage", []Registration{
			{Plugin: a, Before: []framework.TypedName{x.TypedName()}},
			{Plugin: x, Before: []framework.TypedName{a.TypedName()}},
		}, true},
		{"unknown dependency", []Registration{{Plugin: a, After: []framework.TypedName{{Type: "pre", Name: "ghost"}}}}, false},
		{"duplicate", []Registration{{Plugin: a}, {Plugin: pre("a")}}, false},
		{"nil plugin", []Registration{{}}, false},
		{"stageless plugin", []Registration{{Plugin: &lifeHook{name: "l"}}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewTopology(c.regs...)
			if err == nil {
				t.Fatal("topology accepted")
			}
			if errors.Is(err, ErrTopologyCycle) != c.cycle {
				t.Fatalf("err %v, cycle %v", err, c.cycle)
			}
		})
	}
}
