package transfer

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// How often a paused download checks whether its window has passed
const pausePoll = 15 * time.Second

var (
	dayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	dayFull  = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}
)

type window struct {
	days     [7]bool
	from, to int
	rate     uint64
	pause    bool
}

// Bytes per second by time of week, the first window that holds wins
type Schedule struct {
	base    uint64
	windows []window
}

// Compiles config windows over a base rate, refusing days or times it cannot read
func NewSchedule(base uint64, specs []*v1.TransferWindow) (*Schedule, error) {
	s := &Schedule{base: base}
	for i, spec := range specs {
		w := window{rate: spec.GetMaxBytesPerSecond(), pause: spec.GetPause()}
		var err error
		if w.days, err = parseDays(spec.GetDays()); err != nil {
			return nil, fmt.Errorf("window %d: %w", i+1, err)
		}
		if w.from, err = parseClock(spec.GetFrom(), 0); err != nil {
			return nil, fmt.Errorf("window %d from: %w", i+1, err)
		}
		if w.to, err = parseClock(spec.GetTo(), 24*60); err != nil {
			return nil, fmt.Errorf("window %d to: %w", i+1, err)
		}
		s.windows = append(s.windows, w)
	}
	return s, nil
}

// Whether the schedule ever limits or holds anything
func (s *Schedule) Limits() bool {
	return s != nil && (s.base > 0 || len(s.windows) > 0)
}

// Returns the limit in force at t, and whether downloads are held
func (s *Schedule) At(t time.Time) (uint64, bool) {
	if s == nil {
		return 0, false
	}
	minute := t.Hour()*60 + t.Minute()
	day := int(t.Weekday())
	for _, w := range s.windows {
		if w.holds(day, minute) {
			return w.rate, w.pause
		}
	}
	return s.base, false
}

// A window past midnight covers the rest of its day and the start of the next
func (w window) holds(day, minute int) bool {
	if w.from == w.to {
		return w.days[day]
	}
	if w.from < w.to {
		return w.days[day] && minute >= w.from && minute < w.to
	}
	return (w.days[day] && minute >= w.from) || (w.days[(day+6)%7] && minute < w.to)
}

// Reads mon-fri, sat,sun, or nothing for every day
func parseDays(text string) ([7]bool, error) {
	var out [7]bool
	if strings.TrimSpace(text) == "" {
		for i := range out {
			out[i] = true
		}
		return out, nil
	}
	for _, part := range strings.Split(text, ",") {
		lo, hi, ok := strings.Cut(strings.TrimSpace(part), "-")
		a, err := dayIndex(lo)
		if err != nil {
			return out, err
		}
		b := a
		if ok {
			if b, err = dayIndex(hi); err != nil {
				return out, err
			}
		}
		for d := a; ; d = (d + 1) % 7 {
			out[d] = true
			if d == b {
				break
			}
		}
	}
	return out, nil
}

// Reads a day by its three letter name or its full name, nothing looser
func dayIndex(name string) (int, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	for i, d := range dayNames {
		if name == d || name == dayFull[i] {
			return i, nil
		}
	}
	return 0, fmt.Errorf("unknown day %q, use %s", name, strings.Join(dayNames, ", "))
}

// Reads HH:MM as minutes into the day, the fallback when empty
func parseClock(text string, empty int) (int, error) {
	if strings.TrimSpace(text) == "" {
		return empty, nil
	}
	h, m, ok := strings.Cut(strings.TrimSpace(text), ":")
	hour, err := strconv.Atoi(h)
	if err != nil || !ok || hour < 0 || hour > 24 {
		return 0, fmt.Errorf("%q is not HH:MM", text)
	}
	min, err := strconv.Atoi(m)
	if err != nil || min < 0 || min > 59 || hour == 24 && min != 0 {
		return 0, fmt.Errorf("%q is not HH:MM", text)
	}
	return hour*60 + min, nil
}
