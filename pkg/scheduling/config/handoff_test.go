package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/latency"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/profile"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// The pd profile handler takes every registered handoff plugin type by name and refuses any other at validation
func TestPDHandoffParameter(t *testing.T) {
	const anchor = "    threshold: 256\n"
	if !strings.Contains(DefaultPDConfig, anchor) {
		t.Fatal("the default pd configuration no longer sets its threshold where the test names the handoff")
	}
	named := func(handoff string) string {
		return strings.Replace(DefaultPDConfig, anchor, anchor+"    handoff: "+handoff+"\n", 1)
	}
	cases := []struct {
		name string
		yaml string
		want string
		err  error
	}{
		{"none named", DefaultPDConfig, "", nil},
		{"nixl", named(requestcontrol.HandoffNIXL), requestcontrol.HandoffNIXL, nil},
		{"sglang bootstrap", named(requestcontrol.HandoffSGLangBootstrap), requestcontrol.HandoffSGLangBootstrap, nil},
		{"llama.cpp slot", named(requestcontrol.HandoffLlamaSlot), requestcontrol.HandoffLlamaSlot, nil},
		{"unknown", named("mooncake"), "", registry.ErrInvalidParams},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate([]byte(c.yaml))
			if !errors.Is(err, c.err) {
				t.Fatalf("validate %v, want %v", err, c.err)
			}
			if c.err != nil {
				return
			}
			index := prefixcache.New(prefixcache.Config{})
			defer index.Close()
			sched, _, err := Load([]byte(c.yaml), registry.Deps{Store: datalayer.NewStore(), Indexer: index, Predictor: latency.New(latency.Config{})})
			if err != nil {
				t.Fatal(err)
			}
			h, ok := framework.HandlerOf(sched)
			pd, isPD := h.(*profile.PDProfileHandler)
			if !ok || !isPD {
				t.Fatalf("the built handler is %T", h)
			}
			if pd.Handoff() != c.want {
				t.Fatalf("the handler names handoff %q, want %q", pd.Handoff(), c.want)
			}
		})
	}
}
