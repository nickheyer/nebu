package text

import (
	"fmt"
	"strconv"
	"strings"
)

// Coerces any scalar to a float
func Number(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case uint64:
		return float64(n), nil
	case uint32:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case bool:
		if n {
			return 1, nil
		}
		return 0, nil
	case string:
		return ParseNumber(n)
	case nil:
		return 0, fmt.Errorf("nil is not a number")
	}
	return 0, fmt.Errorf("%T is not a number", v)
}

// Parses a number, taking the largest of a comma list, true and false counting as one and zero
func ParseNumber(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty number")
	}
	if strings.Contains(s, ",") {
		best := 0.0
		for i, part := range strings.Split(s, ",") {
			n, err := ParseNumber(part)
			if err != nil {
				return 0, err
			}
			if i == 0 || n > best {
				best = n
			}
		}
		return best, nil
	}
	switch s {
	case "true":
		return 1, nil
	case "false":
		return 0, nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("parse number %q: %w", s, err)
	}
	return n, nil
}

// Parses a byte count written with an inline unit, 5123.45 MiB, the given unit standing in when none is written
func Bytes(s, unit string) (uint64, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	end := 0
	for end < len(s) {
		c := s[end]
		digit := c >= '0' && c <= '9' || c == '.' || c == '-' || c == '+'
		// An e counts as part of the number only when digits follow it, so 5 GiB never eats its unit
		exponent := (c == 'e' || c == 'E') && end+1 < len(s) && (s[end+1] >= '0' && s[end+1] <= '9' || s[end+1] == '-' || s[end+1] == '+')
		if !digit && !exponent {
			break
		}
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("parse bytes %q", s)
	}
	f, err := strconv.ParseFloat(s[:end], 64)
	if err != nil {
		return 0, fmt.Errorf("parse bytes %q: %w", s, err)
	}
	if rest := strings.TrimSpace(s[end:]); rest != "" {
		unit = rest
	}
	mult, ok := unitMultiplier(unit)
	if !ok {
		return 0, fmt.Errorf("unknown unit %q", unit)
	}
	return uint64(f * mult), nil
}

func unitMultiplier(unit string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "", "b", "bytes":
		return 1, true
	case "k", "kb", "kib":
		return 1 << 10, true
	case "m", "mb", "mib":
		return 1 << 20, true
	case "g", "gb", "gib":
		return 1 << 30, true
	case "t", "tb", "tib":
		return 1 << 40, true
	}
	return 0, false
}
