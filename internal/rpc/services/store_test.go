package services

import (
	"context"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
)

// Each filter keeps a model matching any value it lists, the filters narrow together, and an unknown runtime is refused
func TestListModelsFiltersTakeSeveralValues(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	all := runtimes.All()
	rts, err := runtimes.New(all)
	if err != nil {
		t.Fatal(err)
	}
	a, b := all[0].ID(), all[1].ID()
	language := v1.ModelKind_MODEL_KIND_LANGUAGE
	diffusion := v1.ModelKind_MODEL_KIND_DIFFUSION
	component := v1.ModelKind_MODEL_KIND_COMPONENT
	for _, m := range []struct {
		repo, format string
		kind         v1.ModelKind
		runs         uint32
	}{
		{"org/a", "gguf", language, rts.Bit(a)},
		{"org/b", "safetensors", language, rts.Bit(b)},
		{"org/c", "gguf", diffusion, rts.Bit(a) | rts.Bit(b)},
		{"org/d", "onnx", component, 0},
	} {
		if err := st.WriteManifest(&v1.StoredModel{SourceId: "src", Repo: m.repo, Group: "g", FormatId: m.format, Runtimes: m.runs, Descriptor_: &v1.Descriptor{Kind: m.kind}}); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewStoreService(st, nil, nil, nil, rts)
	cases := []struct {
		name string
		req  *v1.ListModelsRequest
		want []string
	}{
		{"no filter", &v1.ListModelsRequest{}, []string{"org/a", "org/b", "org/c", "org/d"}},
		{"one kind", &v1.ListModelsRequest{Kinds: []v1.ModelKind{language}}, []string{"org/a", "org/b"}},
		{"two kinds", &v1.ListModelsRequest{Kinds: []v1.ModelKind{language, component}}, []string{"org/a", "org/b", "org/d"}},
		{"one runtime", &v1.ListModelsRequest{RuntimeIds: []string{a}}, []string{"org/a", "org/c"}},
		{"two runtimes", &v1.ListModelsRequest{RuntimeIds: []string{a, b}}, []string{"org/a", "org/b", "org/c"}},
		{"two formats", &v1.ListModelsRequest{FormatIds: []string{"gguf", "onnx"}}, []string{"org/a", "org/c", "org/d"}},
		{"every filter at once", &v1.ListModelsRequest{Kinds: []v1.ModelKind{language, diffusion}, RuntimeIds: []string{b}, FormatIds: []string{"safetensors", "onnx"}}, []string{"org/b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := svc.ListModels(ctx, connect.NewRequest(c.req))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, m := range resp.Msg.GetModels() {
				got = append(got, m.GetRepo())
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("models %v, want %v", got, c.want)
			}
		})
	}
	if _, err := svc.ListModels(ctx, connect.NewRequest(&v1.ListModelsRequest{RuntimeIds: []string{a, "nope"}})); err == nil || !strings.Contains(err.Error(), `unknown runtime "nope"`) {
		t.Fatalf("an unknown runtime among several: %v", err)
	}
}

// A listing by repository keeps each repository's models side by side in the order of its first, across pages
func TestListModelsGathersRepositories(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rts, err := runtimes.New(runtimes.All())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		source, repo, group string
		bytes               uint64
	}{
		{"src", "org/a", "q4", 40},
		{"src", "org/b", "q8", 90},
		{"src", "org/a", "q8", 80},
		{"src", "org/c", "q4", 60},
		{"other", "org/a", "q2", 20},
		{"src", "org/b", "q2", 10},
	} {
		if err := st.WriteManifest(&v1.StoredModel{SourceId: m.source, Repo: m.repo, Group: m.group, Bytes: m.bytes}); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewStoreService(st, nil, nil, nil, rts)
	list := func(req *v1.ListModelsRequest) []string {
		t.Helper()
		var got []string
		for {
			resp, err := svc.ListModels(ctx, connect.NewRequest(req))
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range resp.Msg.GetModels() {
				got = append(got, m.GetSourceId()+" "+m.GetRepo()+" "+m.GetGroup())
			}
			if req.Cursor = resp.Msg.GetNextCursor(); req.Cursor == "" {
				return got
			}
		}
	}
	size := v1.ModelSort_MODEL_SORT_SIZE
	if got, want := list(&v1.ListModelsRequest{Sort: size}), []string{"src org/b q8", "src org/a q8", "src org/c q4", "src org/a q4", "other org/a q2", "src org/b q2"}; !slices.Equal(got, want) {
		t.Fatalf("by size alone %v, want %v", got, want)
	}
	want := []string{"src org/b q8", "src org/b q2", "src org/a q8", "src org/a q4", "src org/c q4", "other org/a q2"}
	if got := list(&v1.ListModelsRequest{Sort: size, ByRepo: true}); !slices.Equal(got, want) {
		t.Fatalf("by repository %v, want %v", got, want)
	}
	if got := list(&v1.ListModelsRequest{Sort: size, ByRepo: true, Limit: 2}); !slices.Equal(got, want) {
		t.Fatalf("by repository two a page %v, want %v", got, want)
	}
}
