package mesh

import (
	"errors"
	"net/http"
	"testing"
)

// The hop count climbs by one per forward and is refused at the limit or when malformed
func TestNextHop(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   int
		limit  bool
		bad    bool
	}{
		{name: "first forward", want: 1},
		{name: "one crossed", values: []string{"1"}, want: 2},
		{name: "two crossed", values: []string{" 2 "}, want: 3},
		{name: "at the limit", values: []string{"3"}, limit: true},
		{name: "past the limit", values: []string{"9"}, limit: true},
		{name: "negative", values: []string{"-1"}, bad: true},
		{name: "not a number", values: []string{"two"}, bad: true},
		{name: "repeated", values: []string{"1", "1"}, bad: true},
	}
	for _, c := range cases {
		h := http.Header{}
		for _, v := range c.values {
			h.Add(HopHeader, v)
		}
		got, err := NextHop(h)
		switch {
		case c.limit:
			if !errors.Is(err, ErrHopLimit) {
				t.Errorf("%s: %v, want the hop limit", c.name, err)
			}
		case c.bad:
			if err == nil || errors.Is(err, ErrHopLimit) {
				t.Errorf("%s: %v, want a malformed count refused", c.name, err)
			}
		case err != nil || got != c.want:
			t.Errorf("%s: %d %v, want %d", c.name, got, err, c.want)
		}
	}
	if MaxHops != 3 {
		t.Fatalf("the limit is three hops, %d", MaxHops)
	}
}
