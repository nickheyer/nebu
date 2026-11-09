package jinja

import (
	"runtime"
	"testing"
)

// Templates that once grew without bound, the 30 GiB center probe first, each refused while allocating little
func TestMemoryBounds(t *testing.T) {
	const ceiling = 256 << 20
	tests := []struct{ src, kind, msg string }{
		{"{% set q = 5000 %}{{ 'ab'|center(10 ** q) }}", "OverflowError", "Python int too large to convert to C ssize_t"},
		{"{% set q = 5000 %}{{ 'ab'|indent(10 ** q) }}", "OverflowError", "cannot fit 'int' into an index-sized integer"},
		{"{% set q = 5000 %}{{ 'ab'.ljust(10 ** q) }}", "OverflowError", "Python int too large to convert to C ssize_t"},
		{"{% set q = 5000 %}{{ 'a\tb'.expandtabs(10 ** q) }}", "OverflowError", "Python int too large to convert to C int"},
		{"{{ 'ab'|center(2 ** 62) }}", "MemoryError", ""},
		{"{{ 'ab'|center(2 ** 31) }}", "MemoryError", ""},
		{"{{ 'ab'.zfill(2 ** 31) }}", "MemoryError", ""},
		{"{{ 'a\tb'.expandtabs(2 ** 31 - 1) }}", "MemoryError", ""},
		{"{{ ' ' * 2 ** 31 }}", "MemoryError", ""},
		{"{{ [0] * 2 ** 27 }}", "MemoryError", ""},
		{"{{ 'ab'|indent(2 ** 31) }}", "MemoryError", ""},
		{"{{ {'a': 1}|tojson(indent=2 ** 31) }}", "MemoryError", ""},
		{"{{ '%*d' % (2 ** 31, 1) }}", "MemoryError", ""},
		{"{{ '%.*f' % (2 ** 30 + 1, 1.5) }}", "MemoryError", ""},
		{"{{ '{:2147483648}'.format(1) }}", "MemoryError", ""},
		{"{% set s = 'x' * 2 ** 20 %}{{ ([s] * 2048)|join }}", "MemoryError", ""},
		{"{% set s = 'x' * 2 ** 20 %}{{ 'a' ~ s|replace('x', s) }}", "MemoryError", ""},
		{"{% set s = 'x' * 2 ** 20 %}{{ [s] * 2048 }}", "MemoryError", ""},
		{"{% set s = 'x' * (2 ** 26 + 1) %}{{ (s|list)|length }}", "MemoryError", ""},
		{"{{ [1]|batch(2 ** 31, 0)|list }}", "MemoryError", ""},
		{"{{ 3 ** (2 ** 26) > 0 }}", "MemoryError", ""},
		{"{{ 25|round(-(2 ** 40)) }}", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.src, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			out, err := renderSource(tc.src, nil)
			runtime.ReadMemStats(&after)
			if grown := after.TotalAlloc - before.TotalAlloc; grown > ceiling {
				t.Errorf("allocated %d bytes, ceiling %d", grown, ceiling)
			}
			if tc.kind == "" {
				if err != nil || out != "0" {
					t.Fatalf("got %q, %v, want 0", out, err)
				}
				return
			}
			checkExact(t, unitRow{Src: tc.src, Error: tc.kind, Message: tc.msg}, out, err)
		})
	}
}
