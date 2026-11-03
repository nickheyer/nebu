package installs

import (
	"fmt"
	"path"
	"strings"

	"github.com/nickheyer/nebu/pkg/formats/gguf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The stored tokenizer.json of a model, the shallowest when several exist
func TokenizerFile(stored *v1.StoredModel) (string, error) {
	name := stored.GetRepo() + " " + stored.GetGroup()
	if stored.GetFormatId() == (gguf.Format{}).ID() {
		return "", fmt.Errorf("%s carries its tokenizer inside the GGUF weights, not as tokenizer.json", name)
	}
	var best *v1.StoredArtifact
	depth := -1
	for _, sa := range stored.GetArtifacts() {
		rel := sa.GetArtifact().GetPath()
		if path.Base(rel) != "tokenizer.json" {
			continue
		}
		if d := strings.Count(rel, "/"); best == nil || d < depth {
			best, depth = sa, d
		}
	}
	if best == nil {
		return "", fmt.Errorf("%s holds no tokenizer.json", name)
	}
	return best.GetPath(), nil
}
