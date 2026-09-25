package mesh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

const tokenPrefix = "nebu-mesh-"

// What a join token carries: the mesh, its secret, one member to contact, and how to trust it
type joinToken struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Secret      []byte `json:"secret"`
	Address     string `json:"address"`
	TLS         bool   `json:"tls,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func encodeToken(t joinToken) string {
	raw, _ := json.Marshal(t)
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

func decodeToken(s string) (joinToken, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, tokenPrefix) {
		return joinToken{}, fmt.Errorf("%w: a join token starts with %s; a member's Mesh page shows one", ErrMesh, tokenPrefix)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, tokenPrefix))
	if err != nil {
		return joinToken{}, fmt.Errorf("%w: the join token is not readable: %v", ErrMesh, err)
	}
	var t joinToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return joinToken{}, fmt.Errorf("%w: the join token is not readable: %v", ErrMesh, err)
	}
	if t.ID == "" || len(t.Secret) == 0 || t.Address == "" {
		return joinToken{}, fmt.Errorf("%w: the join token names no mesh, secret, or member address", ErrMesh)
	}
	return t, nil
}
