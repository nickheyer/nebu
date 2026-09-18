package bots

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A five field cron line: minute, hour, day of month, month, day of week, each a set of allowed values
type cronLine struct {
	minute, hour, dom, month, dow [64]bool
	// Whether the day fields were both given, in which case either matching is enough, as cron has it
	domAny, dowAny bool
}

// Reads a cron line, taking *, lists, ranges, and steps in every field
func parseCron(s string) (*cronLine, error) {
	fields := strings.Fields(s)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron %q needs five fields: minute hour day month weekday", s)
	}
	c := &cronLine{}
	specs := []struct {
		set      *[64]bool
		min, max int
		names    map[string]int
	}{
		{&c.minute, 0, 59, nil},
		{&c.hour, 0, 23, nil},
		{&c.dom, 1, 31, nil},
		{&c.month, 1, 12, map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}},
		{&c.dow, 0, 7, map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}},
	}
	for i, spec := range specs {
		any, err := parseCronField(fields[i], spec.min, spec.max, spec.names, spec.set)
		if err != nil {
			return nil, fmt.Errorf("cron %q field %d: %w", s, i+1, err)
		}
		switch i {
		case 2:
			c.domAny = any
		case 4:
			c.dowAny = any
		}
	}
	// Sunday is both 0 and 7
	if c.dow[7] {
		c.dow[0] = true
	}
	return c, nil
}

// Fills the set from one field, reporting whether the field was a bare star
func parseCronField(field string, min, max int, names map[string]int, set *[64]bool) (bool, error) {
	star := false
	for _, part := range strings.Split(field, ",") {
		step := 1
		if base, s, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(s)
			if err != nil || n <= 0 {
				return false, fmt.Errorf("step %q", s)
			}
			step, part = n, base
		}
		lo, hi := min, max
		if part == "*" {
			if step == 1 {
				star = true
			}
		} else {
			a, b, ranged := strings.Cut(part, "-")
			var err error
			if lo, err = cronValue(a, names); err != nil {
				return false, err
			}
			hi = lo
			if ranged {
				if hi, err = cronValue(b, names); err != nil {
					return false, err
				}
			} else if step > 1 {
				hi = max
			}
		}
		if lo < min || hi > max || lo > hi {
			return false, fmt.Errorf("%q is outside %d-%d", part, min, max)
		}
		for v := lo; v <= hi; v += step {
			set[v] = true
		}
	}
	return star, nil
}

func cronValue(s string, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("value %q", s)
	}
	return v, nil
}

// Whether the line names this minute
func (c *cronLine) matches(t time.Time) bool {
	if !c.minute[t.Minute()] || !c.hour[t.Hour()] || !c.month[int(t.Month())] {
		return false
	}
	dom := c.dom[t.Day()]
	dow := c.dow[int(t.Weekday())]
	// With both day fields restricted either suffices, as cron has always had it
	if !c.domAny && !c.dowAny {
		return dom || dow
	}
	return dom && dow
}
