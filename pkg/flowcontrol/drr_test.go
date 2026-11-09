package flowcontrol

import (
	"slices"
	"testing"
)

func queue(b *band, fairness string, costs ...int) []*waiter {
	var ws []*waiter
	for _, c := range costs {
		w := &waiter{item: &Item{Cost: c}, cost: chargeOf(&Item{Cost: c})}
		b.add(w, fairness, false)
		ws = append(ws, w)
	}
	return ws
}

func always(*waiter) bool { return true }

func TestBandRoundRobinOrder(t *testing.T) {
	cases := []struct {
		name  string
		flows map[string][]int
		order []string
		want  []string
	}{
		{"equal costs alternate", map[string][]int{"a": {5, 5, 5}, "b": {5, 5, 5}, "c": {5, 5, 5}}, []string{"a", "b", "c"}, []string{"a", "b", "c", "a", "b", "c", "a", "b", "c"}},
		{"cheap flow takes several per turn", map[string][]int{"a": {2, 2, 2, 2, 2, 2}, "b": {7, 7}}, []string{"a", "b"}, []string{"a", "a", "a", "b", "a", "a", "a", "b"}},
		{"zero cost charged one token", map[string][]int{"a": {0, 0, 0, 0}, "b": {1, 1, 1, 1}}, []string{"a", "b"}, []string{"a", "b", "a", "b", "a", "b", "a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBand(0)
			for _, id := range tc.order {
				queue(b, id, tc.flows[id]...)
			}
			var got []string
			for b.n > 0 {
				w := b.pick(always)
				if w == nil {
					t.Fatal("pick returned nothing with items queued")
				}
				got = append(got, w.flow.id)
				w.flow.deficit -= w.cost
				b.remove(w)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("order %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBandDeficitBoundedAfterQuantumShrinks(t *testing.T) {
	b := newBand(0)
	a := queue(b, "a", 600, 1000, 5)
	queue(b, "b", 3, 3)

	take := func(want *waiter, flow string) {
		t.Helper()
		w := b.pick(always)
		if w == nil || w.flow.id != flow || (want != nil && w != want) {
			t.Fatalf("picked %+v, want flow %s", w, flow)
		}
		w.flow.deficit -= w.cost
		b.remove(w)
	}
	take(a[0], "a")
	take(nil, "b")
	if got := b.flows["a"].deficit; got != 1023-600 {
		t.Fatalf("leftover credit %d, want %d", got, 1023-600)
	}
	b.remove(a[1])
	take(nil, "b")

	q := b.quantum()
	w := b.pick(always)
	if w != a[2] {
		t.Fatalf("picked %+v, want the cheap item of flow a", w)
	}
	if d := b.flows["a"].deficit; d > 2*q {
		t.Fatalf("deficit %d exceeds two quanta of %d", d, q)
	}
}

func TestBandSkipsHeldItemsAndResetsIdleCredit(t *testing.T) {
	b := newBand(0)
	held := queue(b, "a", 4, 4)
	free := queue(b, "b", 4)
	b.flows["a"].deficit = 3
	notHeld := func(w *waiter) bool { return !slices.Contains(held, w) }
	if w := b.pick(notHeld); w != free[0] {
		t.Fatalf("picked %+v, want the only eligible item", w)
	}
	if d := b.flows["a"].deficit; d != 0 {
		t.Fatalf("held flow kept credit %d", d)
	}
	free[0].flow.deficit -= free[0].cost
	b.remove(free[0])
	if w := b.pick(notHeld); w != nil {
		t.Fatalf("picked %+v with every item held", w)
	}
}
