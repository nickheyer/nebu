package runtimes

import (
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Lines as vLLM logs them, model.py resolving an auto runner and conversion and entry.py naming the server's tasks
const (
	runnerLine  = "(APIServer pid=7) INFO 11-03 13:18:04 [model.py:1338] Resolved `--runner auto` to `--runner pooling`. Pass the value explicitly to silence this message."
	convertLine = "(APIServer pid=7) INFO 11-03 13:18:04 [model.py:1390] Resolved `--convert auto` to `--convert classify`. Pass the value explicitly to silence this message."
	embedTasks  = "(APIServer pid=7) INFO 11-03 13:18:30 [entry.py:135] Supported tasks: ('embed', 'token_embed')"
	classTasks  = "(APIServer pid=7) INFO 11-03 13:18:30 [entry.py:135] Supported tasks: ('classify', 'token_classify')"
	genTasks    = "(APIServer pid=7) INFO 11-03 13:18:30 [entry.py:135] Supported tasks: ('generate',)"
)

// The runner, conversion and tasks vLLM resolved are read from its log, the unlogged common values once its server started
func TestVLLMResolved(t *testing.T) {
	read := VLLMResolved()
	cases := []struct {
		name                   string
		lines                  []string
		runner, convert, tasks string
		ok                     bool
	}{
		{"generative model", []string{"loading", genTasks}, "generate", "none", "generate", true},
		{"registry pooling model", []string{runnerLine, embedTasks}, "pooling", "none", "embed,token_embed", true},
		{"converted classifier", []string{runnerLine, convertLine, classTasks}, "pooling", "classify", "classify,token_classify", true},
		{"server not started", []string{runnerLine}, "pooling", "", "", false},
		{"nothing logged", []string{"loading weights"}, "", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runner, rok := read[vllmRunner](c.lines)
			convert, cok := read[vllmConvert](c.lines)
			tasks, tok := read[VLLMSupportedTasks](c.lines)
			if runner != c.runner || convert != c.convert || tasks != c.tasks || cok != c.ok || tok != c.ok || rok != (c.runner != "") {
				t.Fatalf("runner %q %v convert %q %v tasks %q %v", runner, rok, convert, cok, tasks, tok)
			}
		})
	}
}

// A vLLM launch serves text and its encoders' media when it generates, else the one pooled output its tasks or conversion name
func TestVLLMModalities(t *testing.T) {
	vision := &v1.Descriptor{Groups: []*v1.TensorGroup{{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER}, {Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION}}}
	cases := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{"generating tasks", map[string]string{VLLMSupportedTasks: "generate"}, "text,image"},
		{"generating with images off", map[string]string{VLLMSupportedTasks: "generate", "limit_mm_per_prompt": `{"image":0}`}, "text"},
		{"embedding tasks", map[string]string{VLLMSupportedTasks: "embed,token_embed", vllmRunner: "pooling", vllmConvert: "none"}, ModalityEmbedding},
		{"classify tasks", map[string]string{VLLMSupportedTasks: "classify,token_classify"}, ModalityClassify},
		{"token tasks alone", map[string]string{VLLMSupportedTasks: "token_embed"}, ModalityPooling},
		{"conversion pinned to embed before start", map[string]string{vllmConvert: "embed"}, ModalityEmbedding},
		{"conversion pinned to classify before start", map[string]string{vllmConvert: "classify"}, ModalityClassify},
		{"pooling runner pinned before start", map[string]string{vllmRunner: "pooling"}, ModalityPooling},
		{"nothing known before start", nil, "text,image"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := VLLMModalities(vision, c.params)
			if err != nil || strings.Join(got, ",") != c.want {
				t.Fatalf("modalities %v %v, want %s", got, err, c.want)
			}
		})
	}
}

// A pinned runner and conversion reach vLLM's command line, empty ones leave vLLM its auto
func TestVLLMRunnerFlags(t *testing.T) {
	profile, plan := fourGPUs()
	for _, c := range []struct {
		params    map[string]string
		want      []string
		forbidden []string
	}{
		{map[string]string{"runner": "pooling", "convert": "embed"}, []string{"--runner pooling", "--convert embed"}, nil},
		{nil, nil, []string{"--runner", "--convert"}},
	} {
		params, err := Resolve(VLLM{}, c.params)
		if err != nil {
			t.Fatal(err)
		}
		cmd, err := VLLM{}.Launch(Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights_dir": "/w"}, Host: "127.0.0.1", Port: 8000, Install: Install{Path: "/venv/bin/vllm"}, Plan: plan, Profile: profile, Descriptor: layered(2), InstallRecord: recorded(nil)})
		if err != nil {
			t.Fatal(err)
		}
		line := strings.Join(cmd.Args, " ")
		for _, w := range c.want {
			if !strings.Contains(line, w) {
				t.Fatalf("%v: %s lacks %s", c.params, line, w)
			}
		}
		for _, f := range c.forbidden {
			if strings.Contains(line, f) {
				t.Fatalf("%v: %s carries %s", c.params, line, f)
			}
		}
	}
}
