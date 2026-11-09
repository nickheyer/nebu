package all

import (
	"testing"
)

// Every format is listed once, and the registry orders them by priority with each reachable by id
func TestRegistryHoldsEveryFormat(t *testing.T) {
	formats := Formats()
	ids := map[string]bool{}
	for _, f := range formats {
		if f.ID() == "" || f.Description() == "" || f.Blurb() == "" {
			t.Fatalf("format %T is unnamed", f)
		}
		if ids[f.ID()] {
			t.Fatalf("format id %s listed twice", f.ID())
		}
		ids[f.ID()] = true
	}
	for _, want := range []string{"gguf", "safetensors", "diffusers", "peft", "nemo", "nemo2", "diffusion"} {
		if !ids[want] {
			t.Fatalf("format %s missing from %v", want, ids)
		}
	}
	reg, err := Registry()
	if err != nil {
		t.Fatal(err)
	}
	list := reg.List()
	if len(list) != len(formats) {
		t.Fatalf("registry lists %d of %d formats", len(list), len(formats))
	}
	for i := 1; i < len(list); i++ {
		if list[i].Priority() > list[i-1].Priority() {
			t.Fatalf("registry order %s before %s", list[i-1].ID(), list[i].ID())
		}
	}
	for id := range ids {
		if reg.Get(id) == nil {
			t.Fatalf("format %s not reachable by id", id)
		}
	}
	if reg.Get("none") != nil {
		t.Fatal("an unknown id must be nil")
	}
}
