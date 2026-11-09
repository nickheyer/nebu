package installs

import (
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/formats/gguf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func artifact(rel, abs string) *v1.StoredArtifact {
	return &v1.StoredArtifact{Artifact: &v1.Artifact{Path: rel}, Path: abs}
}

// The shallowest tokenizer.json wins, GGUF models and models without one are refused by name
func TestTokenizerFile(t *testing.T) {
	cases := []struct {
		name   string
		stored *v1.StoredModel
		want   string
		err    string
	}{
		{"shallowest", &v1.StoredModel{Repo: "org/m", Group: "main", Artifacts: []*v1.StoredArtifact{
			artifact("tokenizer/tokenizer.json", "/s/deep.json"), artifact("tokenizer.json", "/s/top.json"), artifact("config.json", "/s/config.json"),
		}}, "/s/top.json", ""},
		{"nested only", &v1.StoredModel{Repo: "org/m", Group: "main", Artifacts: []*v1.StoredArtifact{artifact("sub/tokenizer.json", "/s/sub.json")}}, "/s/sub.json", ""},
		{"missing", &v1.StoredModel{Repo: "org/m", Group: "q4", Artifacts: []*v1.StoredArtifact{artifact("vocab.json", "/s/vocab.json")}}, "", "org/m q4 holds no tokenizer.json"},
		{"gguf", &v1.StoredModel{Repo: "org/m", Group: "q4", FormatId: (gguf.Format{}).ID(), Artifacts: []*v1.StoredArtifact{artifact("tokenizer.json", "/s/t.json")}}, "", "GGUF weights"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := TokenizerFile(c.stored)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err %v, want %q", err, c.err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %q %v, want %q", got, err, c.want)
			}
		})
	}
}
