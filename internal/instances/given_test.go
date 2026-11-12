package instances

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

// The fake runtime with a param its plan only reckons, one a launch is supplied, and one with a default
type plannedRuntime struct{ fake.Runtime }

const (
	paramReckoned = "reckoned"
	paramSupplied = "supplied"
	paramKept     = "kept"
)

func (plannedRuntime) Params() []*v1.Param {
	return append(fake.Runtime{}.Params(),
		&v1.Param{Name: paramReckoned, Type: v1.ParamType_PARAM_TYPE_INT, Default: runtimes.Auto, Solved: true, Rule: "the runtime's own pick"},
		&v1.Param{Name: paramSupplied, Type: v1.ParamType_PARAM_TYPE_INT, Default: runtimes.Auto, Solved: true, Supplied: true, Rule: "the model's own setting"},
		&v1.Param{Name: paramKept, Type: v1.ParamType_PARAM_TYPE_INT, Default: "7"},
	)
}

func (plannedRuntime) Policy() *estimate.Policy {
	return &estimate.Policy{
		Solve: func(s *estimate.Scope) {
			if s.Params.IsAuto(paramReckoned) {
				s.Params[paramReckoned] = int64(3)
			}
			if s.Params.IsAuto(paramSupplied) {
				s.Params[paramSupplied] = int64(5)
			}
		},
		Deferred: map[string]func([]string) (string, bool){paramKept: logged(paramKept)},
		Stated:   map[string]func([]string) (string, bool){paramReckoned: logged(paramReckoned)},
	}
}

// The template text or file a launch's recorded params name over the model's own
func (plannedRuntime) TemplateOverride(params map[string]string) (string, string) {
	return params["chat_template"], params["chat_template_file"]
}

// Reads the value a log line gives a name, written name=value
func logged(name string) func([]string) (string, bool) {
	return func(lines []string) (string, bool) {
		for _, line := range lines {
			if v, ok := strings.CutPrefix(line, name+"="); ok {
				return v, true
			}
		}
		return "", false
	}
}

// A manager whose one runtime plans params the launch must not all take
func plannedManager(t *testing.T, ctx context.Context) *Manager {
	t.Helper()
	m := newSeatNode(t, ctx).manager(t, ctx, nil)
	rts, err := runtimes.New([]runtimes.Runtime{plannedRuntime{}})
	if err != nil {
		t.Fatal(err)
	}
	m.Runtimes = rts
	return m
}

// A launch passes what was set and what the model supplies, never a value the plan reckoned or a default
func TestLaunchPassesOnlyWhatIsSet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := plannedManager(t, ctx)
	run := &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, Name: "plain"}
	p, err := m.prepare(ctx, run, "")
	if err != nil {
		t.Fatal(err)
	}
	if plan := p.plan.GetParams(); plan[paramReckoned] != "3" || plan[paramSupplied] != "5" || plan[paramKept] != "7" {
		t.Fatalf("the plan reckons every unset param: %v", plan)
	}
	_, passed, err := m.Preview(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, reckoned := passed[paramReckoned]; reckoned || passed[paramKept] != "" || passed[paramSupplied] != "5" {
		t.Fatalf("an unset launch takes the supplied param alone: %v", passed)
	}
	run.Params = map[string]string{paramReckoned: "9", paramSupplied: "1", paramKept: "7"}
	if _, passed, err = m.Preview(ctx, run); err != nil || passed[paramReckoned] != "9" || passed[paramSupplied] != "1" || passed[paramKept] != "7" {
		t.Fatalf("set params are passed as set, one equal to its default too: %v %v", passed, err)
	}
	for _, unset := range []string{"", "auto", " AUTO "} {
		run.Params = map[string]string{paramReckoned: unset}
		if _, passed, err = m.Preview(ctx, run); err != nil {
			t.Fatal(err)
		} else if _, reckoned := passed[paramReckoned]; reckoned {
			t.Fatalf("%q leaves the param unset: %v", unset, passed)
		}
	}
}

// A seat runs its formation's plan, taking the plan's value for each solved param its request leaves unset
func TestSeatTakesItsFormationPlan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := plannedManager(t, ctx)
	run := seatRun(runtimes.RoleHead, 0, false, t.TempDir())
	run.Seat.Memory = &v1.MemoryPlan{Params: map[string]string{paramReckoned: "4", paramSupplied: "6", paramKept: "7"}}
	p, err := m.prepare(ctx, run, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.params.Int(paramReckoned) != 4 || p.params.Int(paramSupplied) != 6 || p.params[paramKept] != nil {
		t.Fatalf("the seat takes the plan's solved params and no default: %v", p.params)
	}
	run.Params = map[string]string{paramReckoned: "2"}
	if p, err = m.prepare(ctx, run, ""); err != nil || p.params.Int(paramReckoned) != 2 {
		t.Fatalf("a param the seat's request sets stands over the plan: %v %v", p.params, err)
	}
}

// What the runtime picked for unset params is recorded from its log, a param it logs only sometimes without complaint
func TestChosenRecordsWhatTheLogStates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := plannedManager(t, ctx)
	var notes []string
	logf := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }
	in := m.newInstance(&v1.Instance{Id: "i1", Name: "plain", RuntimeId: fake.RuntimeID, Params: map[string]string{paramSupplied: "5"}}, plannedRuntime{})
	if picked := m.chosen(logf, in, []string{"loading"}); len(picked) != 0 {
		t.Fatalf("a log stating nothing records nothing: %v", picked)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], paramKept) {
		t.Fatalf("only the param the runtime always logs is missed aloud: %q", notes)
	}
	picked := m.chosen(logf, in, []string{"kept=11", "reckoned=21", "supplied=99"})
	rec := in.snapshot().GetParams()
	if picked[paramKept] != "11" || picked[paramReckoned] != "21" || len(picked) != 2 || rec[paramKept] != "11" || rec[paramReckoned] != "21" || rec[paramSupplied] != "5" {
		t.Fatalf("picked %v recorded %v", picked, rec)
	}
	set := m.newInstance(&v1.Instance{Id: "i2", Name: "set", RuntimeId: fake.RuntimeID, Params: map[string]string{paramReckoned: "9"}}, plannedRuntime{})
	if picked := m.chosen(logf, set, []string{"reckoned=21"}); len(picked) != 0 || set.snapshot().GetParams()[paramReckoned] != "9" {
		t.Fatalf("a param the launch set is not read back: %v", picked)
	}
}

// The template inspected is the one the launch runs, its params' file or text over the model's own
func TestTemplateFollowsTheLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := plannedManager(t, ctx)
	rec := func(params map[string]string) *v1.Instance {
		return &v1.Instance{Id: "i1", Name: "plain", RuntimeId: fake.RuntimeID, SourceId: "src", Repo: "org/model", Group: "q4", Params: params}
	}
	own, err := m.launchTemplate(rec(nil))
	if err != nil || !strings.Contains(own.Text, "tools | tojson") || !own.Tools() {
		t.Fatalf("no override reads the model's own template: %v %v", own, err)
	}
	file := filepath.Join(t.TempDir(), "plain.jinja")
	if err := os.WriteFile(file, []byte("{{ messages }}"), 0o644); err != nil {
		t.Fatal(err)
	}
	named, err := m.launchTemplate(rec(map[string]string{"chat_template_file": file}))
	if err != nil || named.Text != "{{ messages }}" || named.Tools() {
		t.Fatalf("a template file the launch names is the one read: %v %v", named, err)
	}
	if tp := chattemplate.Describe(named, err); tp.GetTools() != v1.ToolSupport_TOOL_SUPPORT_EMULATED || tp.GetSource() != file {
		t.Fatalf("the record follows the override: %v", tp)
	}
	inline, err := m.launchTemplate(rec(map[string]string{"chat_template": "{% for m in messages %}{{ m.content }}{% endfor %}"}))
	if err != nil || !strings.Contains(inline.Text, "m.content") {
		t.Fatalf("jinja text the launch names is the one read: %v %v", inline, err)
	}
	if _, err := m.launchTemplate(rec(map[string]string{"chat_template": "chatml"})); err == nil || !strings.Contains(err.Error(), "chatml") {
		t.Fatalf("a built in template leaves no text to read: %v", err)
	}
	if tp := chattemplate.Describe(m.launchTemplate(rec(map[string]string{"chat_template": "chatml"}))); tp.GetError() == "" || chattemplate.Probed(tp) {
		t.Fatalf("a template that cannot be read records no reasoning settings: %v", tp)
	}
}
