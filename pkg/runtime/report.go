package runtime

import (
	"regexp"

	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

type reportRule struct {
	spec *v1.ReportRule
	re   *regexp.Regexp
}

// Sums allocations the runtime reported, keyed by rule
func (rt *Runtime) Measure(lines []string) []*v1.Measurement {
	var out []*v1.Measurement
	byKey := map[string]*v1.Measurement{}
	for _, r := range rt.report {
		for _, line := range lines {
			m := r.re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			var value, unit string
			for i, name := range r.re.SubexpNames() {
				switch name {
				case "value":
					value = m[i]
				case "unit":
					unit = m[i]
				}
			}
			if unit == "" {
				unit = r.spec.GetUnit()
			}
			bytes, err := eval.Bytes(value, unit)
			if err != nil {
				continue
			}
			ms, ok := byKey[r.spec.GetKey()]
			if !ok {
				ms = &v1.Measurement{Key: r.spec.GetKey(), Line: line}
				byKey[r.spec.GetKey()] = ms
				out = append(out, ms)
			}
			ms.Bytes += bytes
		}
	}
	return out
}
