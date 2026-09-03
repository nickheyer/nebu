package build

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

const idLength = 16

// Hashes everything that decides the bytes a build produces
func hashBuild(b *v1.Build, spec *v1.Recipe, patches map[string][]byte) string {
	h := sha256.New()
	write := func(parts ...string) {
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
	}
	opts := proto.MarshalOptions{Deterministic: true}
	if data, err := opts.Marshal(spec); err == nil {
		h.Write(data)
	}
	write("variant", b.GetVariant(), "ref", b.GetRef(), "sandbox", eval.EnumShort(b.GetSandbox()), "image", b.GetImage())
	write("vars")
	write(sortedPairs(b.GetVars())...)
	write("facts")
	write(sortedPairs(b.GetFacts())...)
	write("patches")
	for _, id := range b.GetPatches() {
		write(id)
		h.Write(patches[id])
	}
	return hex.EncodeToString(h.Sum(nil))[:idLength]
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
