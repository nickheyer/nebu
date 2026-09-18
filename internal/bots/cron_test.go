package bots

import (
	"testing"
	"time"
)

func TestCron(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		line  string
		yes   []string
		no    []string
		fails bool
	}{
		{line: "* * * * *", yes: []string{"2026-01-05 00:00", "2026-12-31 23:59"}},
		{line: "0 9 * * mon-fri", yes: []string{"2026-01-05 09:00"}, no: []string{"2026-01-05 09:01", "2026-01-04 09:00"}},
		{line: "*/15 * * * *", yes: []string{"2026-01-05 10:00", "2026-01-05 10:45"}, no: []string{"2026-01-05 10:10"}},
		{line: "30 18 1,15 * *", yes: []string{"2026-03-01 18:30", "2026-03-15 18:30"}, no: []string{"2026-03-02 18:30"}},
		{line: "0 0 * jan,jul sun", yes: []string{"2026-01-04 00:00"}, no: []string{"2026-02-01 00:00"}},
		// Both day fields restricted: either matching is enough
		{line: "0 12 13 * fri", yes: []string{"2026-02-13 12:00", "2026-01-09 12:00"}, no: []string{"2026-01-08 12:00"}},
		{line: "0 0 * * 7", yes: []string{"2026-01-04 00:00"}},
		{line: "60 * * * *", fails: true},
		{line: "* * * *", fails: true},
		{line: "a * * * *", fails: true},
	}
	for _, c := range cases {
		line, err := parseCron(c.line)
		if c.fails {
			if err == nil {
				t.Fatalf("%q parsed", c.line)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		for _, y := range c.yes {
			if !line.matches(at(y)) {
				t.Fatalf("%q should match %s", c.line, y)
			}
		}
		for _, n := range c.no {
			if line.matches(at(n)) {
				t.Fatalf("%q should not match %s", c.line, n)
			}
		}
	}
}
