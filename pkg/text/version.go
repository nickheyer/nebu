package text

import (
	"strconv"
	"strings"
)

// Compares dotted numeric versions, ignoring anything after the numbers, so 580.65 sorts above 550
func CompareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(s string) []int {
	var parts []int
	for _, field := range strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' || r == '+' }) {
		digits := strings.TrimLeftFunc(field, func(r rune) bool { return r < '0' || r > '9' })
		digits = strings.TrimRightFunc(digits, func(r rune) bool { return r < '0' || r > '9' })
		n, err := strconv.Atoi(digits)
		if err != nil {
			break
		}
		parts = append(parts, n)
	}
	return parts
}
