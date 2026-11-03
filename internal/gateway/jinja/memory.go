package jinja

import (
	"math/big"
	"strings"
	"unicode/utf8"
)

// Bytes of the largest value the engine builds before failing with MemoryError as an exhausted Python would
const maxValueBytes = 1 << 30

// Bytes one compile or render allocates in all before failing with MemoryError
const allocBudget = 2 << 30

// Bits of the largest integer arithmetic builds before failing with MemoryError
const maxIntBits = 1 << 25

// Bytes a list, tuple or dict slot and a fresh variable scope occupy
const (
	slotBytes  = 16
	scopeBytes = 64
)

// Python's MemoryError, which carries no message
func memoryError() error { return errorf("MemoryError", "") }

// Bytes a value holds itself, its text, its slots or its digits
func sizeOf(v Value) int {
	switch x := v.(type) {
	case string:
		return len(x)
	case Markup:
		return len(x)
	case *List:
		return len(x.items) * slotBytes
	case *Tuple:
		return len(x.items) * slotBytes
	case *Dict:
		return 2 * len(x.keys) * slotBytes
	case *big.Int:
		return x.BitLen() / 8
	}
	return 0
}

// Refuses building a value of n bytes past the largest the engine builds
func checkBytes(n int) error {
	if n < 0 || n > maxValueBytes {
		return memoryError()
	}
	return nil
}

// Refuses text of n characters of a fill rune past the largest value the engine builds
func checkAlloc(n int) error {
	if n > maxValueBytes {
		return memoryError()
	}
	return nil
}

// Refuses filling width characters with a rune past the largest value the engine builds
func checkFill(width int, fill rune) error {
	if width > maxValueBytes/max(utf8.RuneLen(fill), 1) {
		return memoryError()
	}
	return nil
}

// Charges n bytes to the allocation budget, failing once the budget is spent
func (r *renderer) charge(n int) error {
	if err := checkBytes(n); err != nil {
		return err
	}
	r.allocated += n
	if r.allocated > allocBudget {
		return memoryError()
	}
	return nil
}

// Charges the bytes a freshly built value holds
func (r *renderer) chargeValue(v Value) error { return r.charge(sizeOf(v)) }

// Refuses joining parts with a separator into text past the largest value the engine builds
func checkJoin(parts []string, sep string) error {
	n := len(sep) * max(len(parts)-1, 0)
	for _, p := range parts {
		n += len(p)
		if n > maxValueBytes {
			return memoryError()
		}
	}
	return checkBytes(n)
}

// str.replace, refused when the result would pass the largest value the engine builds
func replaceChecked(s, old, repl string, count int) (string, error) {
	hits := utf8.RuneCountInString(s) + 1
	if old != "" {
		hits = strings.Count(s, old)
	}
	if count >= 0 {
		hits = min(hits, count)
	}
	if growth := len(repl) - len(old); growth > 0 && hits > (maxValueBytes-len(s))/growth {
		return "", memoryError()
	}
	return pyReplace(s, old, repl, count), nil
}

// A lower bound on a value's repr length, found without building it and saturating past the value limit
func reprFloor(v Value) int {
	var items []Value
	switch x := v.(type) {
	case string:
		return len(x) + 2
	case Markup:
		return len(x) + 10
	case *List:
		items = x.items
	case *Tuple:
		items = x.items
	case *Dict:
		items = append(append(items, x.keys...), x.vals...)
	default:
		return 1
	}
	n := 2
	for _, it := range items {
		n += reprFloor(it) + 2
		if n > maxValueBytes {
			return n
		}
	}
	return n
}
