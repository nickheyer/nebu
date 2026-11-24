package pools

import (
	"strings"
	"testing"
	"time"
)

// A condition that may cost more than a condition may is refused on saving, while one stored before conditions were
// costed keeps deciding as it did, loops over written lists within the cost saving as ever
func TestStoredConditionOverItsCostKeepsDecidingAndIsRefusedOnSaving(t *testing.T) {
	m := &Manager{}
	list := "[1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12]"
	costly := list + ".all(a, " + list + ".all(b, " + list + ".all(c, a + b + c < 1000)))"
	for _, within := range []string{"ready > 0", "free >= 2 && now.getHours('America/Los_Angeles') < 18", "[1, 2, 3].exists(n, n == ready)"} {
		if _, err := m.condition(within, true); err != nil {
			t.Fatalf("%s is refused on saving: %v", within, err)
		}
	}
	if _, err := m.condition(costly, true); err == nil || !strings.Contains(err.Error(), "may cost up to") {
		t.Fatalf("a condition over its cost saved: %v", err)
	}
	// Stored, it decides as it did before conditions were costed, however often it is asked
	for range 2 {
		p, err := m.condition(costly, false)
		if err != nil || p == nil {
			t.Fatalf("a stored condition over its cost stopped deciding: %v", err)
		}
		out, _, err := p.Eval(map[string]any{"now": time.Now(), "ready": 1, "free": 1})
		if held, _ := out.Value().(bool); err != nil || !held {
			t.Fatalf("a stored condition over its cost answered %v %v", out, err)
		}
	}
	if _, err := m.condition(costly, true); err == nil {
		t.Fatal("a stored condition over its cost saved once it had decided")
	}
	for _, saving := range []bool{true, false} {
		if _, err := m.condition("ready >", saving); err == nil {
			t.Fatal("a condition that does not parse compiled")
		}
		if _, err := m.condition("ready + 1", saving); err == nil || !strings.Contains(err.Error(), "not true or false") {
			t.Fatalf("a condition answering a number compiled: %v", err)
		}
	}
	if p, err := m.condition("", true); p != nil || err != nil {
		t.Fatalf("an empty condition %v %v", p, err)
	}
}
