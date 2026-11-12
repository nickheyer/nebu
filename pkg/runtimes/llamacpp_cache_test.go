package runtimes

import (
	"errors"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/estimate"
)

// The cache and slot params render as llama-server spells them, switches as --flag or --no-flag, none passed while unset
func TestLlamaCppCacheFlags(t *testing.T) {
	rt := LlamaCpp{}
	plain := layered(4)
	for _, c := range []struct {
		name      string
		overrides map[string]string
		want      string
		emitted   map[string]string
	}{
		{name: "nothing set", emitted: map[string]string{"metrics": "true"}},
		{name: "ram cache and similarity", overrides: map[string]string{"cache_ram": "32768", "slot_prompt_similarity": "0.5"}, want: "--slot-prompt-similarity 0.5 --cache-ram 32768", emitted: map[string]string{"cache_ram": "32768", "slot_prompt_similarity": "0.5"}},
		{name: "checkpoints", overrides: map[string]string{"ctx_checkpoints": "64", "checkpoint_min_step": "2048", "cache_reuse": "512"}, want: "--ctx-checkpoints 64 --checkpoint-min-step 2048 --cache-reuse 512"},
		{name: "reuse off", overrides: map[string]string{"cache_reuse": "0"}, want: "--cache-reuse 0", emitted: map[string]string{"cache_reuse": "0"}},
		{name: "unified pool", overrides: map[string]string{"kv_unified": "true"}, want: "--kv-unified", emitted: map[string]string{"kv_unified": "on"}},
		{name: "pool split by slot", overrides: map[string]string{"kv_unified": "off"}, want: "--no-kv-unified", emitted: map[string]string{"kv_unified": "off"}},
		{name: "idle slots on", overrides: map[string]string{"cache_idle_slots": "on"}, want: "--cache-idle-slots", emitted: map[string]string{"cache_idle_slots": "on"}},
		{name: "idle slots off", overrides: map[string]string{"cache_idle_slots": "off"}, want: "--no-cache-idle-slots", emitted: map[string]string{"cache_idle_slots": "off"}},
		{name: "prompt caching off", overrides: map[string]string{"cache_prompt": "off"}, want: "--no-cache-prompt", emitted: map[string]string{"cache_prompt": "off"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			params, err := Resolve(rt, c.overrides)
			if err != nil {
				t.Fatal(err)
			}
			cmd, err := rt.Launch(Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights": "/w.gguf"}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/llama-server"}, Descriptor: plain, InstallRecord: recorded(map[string]string{})})
			if err != nil {
				t.Fatal(err)
			}
			args := " " + strings.Join(cmd.Args, " ") + " "
			for _, flag := range []string{"--cache-ram", "--slot-prompt-similarity", "--ctx-checkpoints", "--checkpoint-min-step", "--cache-reuse", "--kv-unified", "--no-kv-unified", "--cache-idle-slots", "--no-cache-idle-slots", "--cache-prompt", "--no-cache-prompt", "--batch-size", "--ubatch-size", "--threads", "--parallel", "--flash-attn", "--log-verbosity", "--n-gpu-layers", "--n-cpu-moe", "--ctx-size", "--jinja"} {
				if strings.Contains(args, " "+flag+" ") != strings.Contains(" "+c.want+" ", " "+flag+" ") {
					t.Fatalf("flags %q, want %q among them", args, c.want)
				}
			}
			for _, pair := range strings.Split(c.want, " --") {
				if pair != "" && !strings.Contains(args, " --"+strings.TrimPrefix(pair, "--")+" ") {
					t.Fatalf("flags %q lack %q", args, pair)
				}
			}
			for k, v := range c.emitted {
				if cmd.Params[k] != v {
					t.Fatalf("emitted %v lacks %s=%s", cmd.Params, k, v)
				}
			}
			if _, ok := cmd.Params["cache_idle_slots"]; ok && c.emitted["cache_idle_slots"] == "" {
				t.Fatalf("an unset switch is left to llama.cpp, got %v", cmd.Params)
			}
			if !strings.Contains(args, " --metrics ") {
				t.Fatalf("the metrics endpoint the gateway reads is always on: %q", args)
			}
		})
	}
}

// Parallel sequences split the context pool unless the pool is unified, as llama.cpp divides --ctx-size
func TestLlamaCppSequencesSplitTheContext(t *testing.T) {
	for _, c := range []struct {
		params estimate.Params
		want   int64
	}{
		{params: estimate.Params{}, want: 1},
		{params: estimate.Params{"n_parallel": int64(3)}, want: 3},
		{params: estimate.Params{"n_parallel": int64(3), "kv_unified": "on"}, want: 1},
		{params: estimate.Params{"n_parallel": int64(0)}, want: 1},
		{params: estimate.Params{"n_parallel": int64(-1)}, want: 1},
		{params: estimate.Params{"n_parallel": int64(-1), "kv_unified": "off"}, want: 1},
	} {
		if got := llamaSequences(&estimate.Scope{Params: c.params}); got != c.want {
			t.Errorf("%v: %d sequences, want %d", c.params, got, c.want)
		}
	}
}

// Zero parallel sequences is refused before llama.cpp can exit on it, -1 and counts pass
func TestLlamaParallelRefusesZero(t *testing.T) {
	for raw, ok := range map[string]bool{"0": false, "-1": true, "1": true, "8": true} {
		_, err := Resolve(LlamaCpp{}, map[string]string{"n_parallel": raw})
		if ok && err != nil {
			t.Errorf("n_parallel %s: %v", raw, err)
		}
		if !ok && !errors.Is(err, ErrParam) {
			t.Errorf("n_parallel %s: err = %v, want ErrParam", raw, err)
		}
	}
}
