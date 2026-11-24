package build

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/recipes"
	"github.com/nickheyer/nebu/pkg/text"
)

const idLength = 16

// Hashes resolved build inputs: steps, outputs, binary, variant, ref, sandbox, image, variables,
// facts, and patches, each the request's own where it gave one, and the repository and environment
// of a request that named any. A request that names neither hashes as it did before it could
func hashBuild(s *Selection, b *v1.Build, bctx *recipes.Build, patches map[string][]byte) (string, error) {
	rc := s.Recipe
	h := sha256.New()
	write := func(parts ...string) {
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
	}
	write("recipe", rc.ID(), rc.RuntimeID())
	steps, err := s.Steps(bctx)
	if err != nil {
		return "", err
	}
	for _, st := range steps {
		write("step", st.Name, st.Dir)
		write(st.Command...)
		write(sortedPairs(st.Env)...)
	}
	write("outputs")
	write(s.Outputs()...)
	write("binary", s.Binary())
	if s.repo != "" {
		write("repo", s.repo)
	}
	if len(s.Env) > 0 {
		write("env")
		write(sortedPairs(s.Env)...)
	}
	write("variant", b.GetVariant(), "ref", b.GetRef(), "sandbox", text.Enum(b.GetSandbox()), "image", b.GetImage())
	write("vars")
	write(sortedPairs(b.GetVars())...)
	write("facts")
	write(sortedPairs(b.GetFacts())...)
	write("patches")
	for _, id := range b.GetPatches() {
		write(id)
		h.Write(patches[id])
	}
	return hex.EncodeToString(h.Sum(nil))[:idLength], nil
}

func sortedPairs(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+strings.TrimSpace(m[k]))
	}
	return out
}
