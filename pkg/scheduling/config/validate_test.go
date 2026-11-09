package config

import (
	"errors"
	"testing"
)

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want error
	}{
		{"default", DefaultConfig, nil},
		{"no metrics", DefaultNoMetricsConfig, nil},
		{"pd", DefaultPDConfig, nil},
		{"unknown field", "plugins: []\nbogus: 1\n", ErrInvalid},
		{"unknown handler", "plugins:\n- type: ready-filter\n  name: ready\nprofiles: []\nprofileHandler:\n  pluginRef: missing\n", ErrUnknownRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate([]byte(tc.yaml))
			if tc.want == nil && err != nil {
				t.Fatalf("valid config refused: %v", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}
