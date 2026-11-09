package jinja

import (
	"strconv"
	"strings"
	"time"
)

var (
	weekdayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	monthNames   = []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
)

// Formats a naive local time as Python's datetime.strftime does on glibc in the C locale
func pyStrftime(t time.Time, format string) string {
	var b strings.Builder
	rs := []rune(format)
	for i := 0; i < len(rs); i++ {
		if rs[i] != '%' {
			b.WriteRune(rs[i])
			continue
		}
		start := i
		i++
		flag := rune(0)
		for i < len(rs) && strings.ContainsRune("_-0^#", rs[i]) {
			flag = rs[i]
			i++
		}
		width := -1
		ws := i
		for i < len(rs) && rs[i] >= '0' && rs[i] <= '9' {
			i++
		}
		if i > ws {
			width, _ = strconv.Atoi(string(rs[ws:i]))
		}
		if i < len(rs) && (rs[i] == 'E' || rs[i] == 'O') {
			i++
		}
		if i >= len(rs) {
			b.WriteString(string(rs[start:]))
			break
		}
		if rs[i] == ':' && i+1 < len(rs) && rs[i+1] == 'z' {
			i++
			continue
		}
		s, numeric, ok := strftimeField(t, rs[i])
		if !ok {
			b.WriteString(string(rs[start : i+1]))
			continue
		}
		b.WriteString(applyStrftimeFlag(s, numeric, flag, width, rs[i]))
	}
	return b.String()
}

// The text of one directive, whether it is numeric with its default pad, and whether it is known
func strftimeField(t time.Time, c rune) (string, int, bool) {
	pad := func(n, w int) string {
		s := strconv.Itoa(n)
		for len(s) < w {
			s = "0" + s
		}
		return s
	}
	hour12 := t.Hour() % 12
	if hour12 == 0 {
		hour12 = 12
	}
	year, week := t.ISOWeek()
	switch c {
	case 'a':
		return weekdayNames[t.Weekday()][:3], 0, true
	case 'A':
		return weekdayNames[t.Weekday()], 0, true
	case 'b', 'h':
		return monthNames[t.Month()-1][:3], 0, true
	case 'B':
		return monthNames[t.Month()-1], 0, true
	case 'c':
		return pyStrftime(t, "%a %b %e %H:%M:%S %Y"), 0, true
	case 'C':
		return pad(t.Year()/100, 2), 2, true
	case 'd':
		return pad(t.Day(), 2), 2, true
	case 'D':
		return pyStrftime(t, "%m/%d/%y"), 0, true
	case 'e':
		return padSpace(t.Day(), 2), -2, true
	case 'f':
		return pad(t.Nanosecond()/1000, 6), 6, true
	case 'F':
		return pyStrftime(t, "%Y-%m-%d"), 0, true
	case 'g':
		return pad(year%100, 2), 2, true
	case 'G':
		return strconv.Itoa(year), 1, true
	case 'H':
		return pad(t.Hour(), 2), 2, true
	case 'I':
		return pad(hour12, 2), 2, true
	case 'j':
		return pad(t.YearDay(), 3), 3, true
	case 'k':
		return padSpace(t.Hour(), 2), -2, true
	case 'l':
		return padSpace(hour12, 2), -2, true
	case 'm':
		return pad(int(t.Month()), 2), 2, true
	case 'M':
		return pad(t.Minute(), 2), 2, true
	case 'n':
		return "\n", 0, true
	case 'p':
		if t.Hour() < 12 {
			return "AM", 0, true
		}
		return "PM", 0, true
	case 'P':
		if t.Hour() < 12 {
			return "am", 0, true
		}
		return "pm", 0, true
	case 'r':
		return pyStrftime(t, "%I:%M:%S %p"), 0, true
	case 'R':
		return pyStrftime(t, "%H:%M"), 0, true
	case 's':
		return strconv.FormatInt(t.Unix(), 10), 1, true
	case 'S':
		return pad(t.Second(), 2), 2, true
	case 't':
		return "\t", 0, true
	case 'T':
		return pyStrftime(t, "%H:%M:%S"), 0, true
	case 'u':
		wd := int(t.Weekday())
		if wd == 0 {
			wd = 7
		}
		return strconv.Itoa(wd), 1, true
	case 'U':
		return pad((t.YearDay()+6-int(t.Weekday()))/7, 2), 2, true
	case 'V':
		return pad(week, 2), 2, true
	case 'w':
		return strconv.Itoa(int(t.Weekday())), 1, true
	case 'W':
		return pad((t.YearDay()+6-(int(t.Weekday())+6)%7)/7, 2), 2, true
	case 'x':
		return pyStrftime(t, "%m/%d/%y"), 0, true
	case 'X':
		return pyStrftime(t, "%H:%M:%S"), 0, true
	case 'y':
		return pad(t.Year()%100, 2), 2, true
	case 'Y':
		return strconv.Itoa(t.Year()), 1, true
	case 'z', 'Z':
		return "", 0, true
	case '%':
		return "%", 0, true
	}
	return "", 0, false
}

func padSpace(n, w int) string {
	s := strconv.Itoa(n)
	for len(s) < w {
		s = " " + s
	}
	return s
}

// Applies glibc's padding and case flags and field width to one directive's text
func applyStrftimeFlag(s string, numeric int, flag rune, width int, conv rune) string {
	if flag == '^' {
		s = strings.ToUpper(s)
	}
	if flag == '#' {
		switch conv {
		case 'p':
			s = strings.ToLower(s)
		case 'a', 'A', 'b', 'B', 'h':
			s = strings.ToUpper(s)
		}
	}
	if numeric != 0 {
		digits := strings.TrimLeft(s, " 0")
		if digits == "" {
			digits = "0"
		}
		neg := strings.HasPrefix(s, "-")
		if neg {
			digits = strings.TrimLeft(s[1:], " 0")
		}
		natural := numeric
		if natural < 0 {
			natural = -natural
		}
		switch flag {
		case '-':
			s = digits
		case '_':
			s = padTo(digits, natural, ' ')
		case '0':
			s = padTo(digits, natural, '0')
		}
		if neg && flag != 0 {
			s = "-" + s
		}
		if width > 0 {
			fill := byte('0')
			if flag == '_' || (flag != '0' && numeric < 0) {
				fill = ' '
			}
			if flag == '-' {
				return s
			}
			s = padTo(strings.TrimLeft(s, " "), width, fill)
		}
		return s
	}
	if width > 0 {
		fill := byte(' ')
		if flag == '0' {
			fill = '0'
		}
		s = padTo(s, width, fill)
	}
	return s
}

func padTo(s string, w int, fill byte) string {
	for len(s) < w {
		s = string(fill) + s
	}
	return s
}
