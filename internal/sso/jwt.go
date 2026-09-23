package sso

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rsa"
	_ "crypto/sha256"
	_ "crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Signature algorithms and their digests. EdDSA hashes internally.
var hashes = map[string]crypto.Hash{
	"HS256": crypto.SHA256, "HS384": crypto.SHA384, "HS512": crypto.SHA512,
	"RS256": crypto.SHA256, "RS384": crypto.SHA384, "RS512": crypto.SHA512,
	"PS256": crypto.SHA256, "PS384": crypto.SHA384, "PS512": crypto.SHA512,
	"ES256": crypto.SHA256, "ES384": crypto.SHA384, "ES512": crypto.SHA512,
}

var (
	errBadSignature = errors.New("signature does not verify")
	// No published key matches the token, the set may have rotated
	errUnknownKey = errors.New("no published key matches the token")
)

// One key from the provider's JWK set
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Crv string `json:"crv"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// Decodes the key's public half
func (k jwk) public() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err := bigInt(k.N)
		if err != nil {
			return nil, fmt.Errorf("rsa modulus: %w", err)
		}
		e, err := bigInt(k.E)
		if err != nil {
			return nil, fmt.Errorf("rsa exponent: %w", err)
		}
		if !e.IsInt64() || e.Int64() < 3 {
			return nil, errors.New("rsa exponent out of range")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("unsupported curve %q", k.Crv)
		}
		x, err := bigInt(k.X)
		if err != nil {
			return nil, fmt.Errorf("ec x: %w", err)
		}
		y, err := bigInt(k.Y)
		if err != nil {
			return nil, fmt.Errorf("ec y: %w", err)
		}
		return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
	case "OKP":
		if k.Crv != "Ed25519" {
			return nil, fmt.Errorf("unsupported curve %q", k.Crv)
		}
		raw, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, fmt.Errorf("okp x: %w", err)
		}
		if len(raw) != ed25519.PublicKeySize {
			return nil, errors.New("ed25519 key has the wrong size")
		}
		return ed25519.PublicKey(raw), nil
	}
	return nil, fmt.Errorf("unsupported key type %q", k.Kty)
}

// Key type an algorithm needs
func keyType(alg string) string {
	switch {
	case strings.HasPrefix(alg, "RS"), strings.HasPrefix(alg, "PS"):
		return "RSA"
	case strings.HasPrefix(alg, "ES"):
		return "EC"
	case alg == "EdDSA":
		return "OKP"
	}
	return ""
}

func bigInt(s string) (*big.Int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("empty")
	}
	return new(big.Int).SetBytes(raw), nil
}

// A parsed compact JWS with its claims
type token struct {
	alg    string
	kid    string
	claims map[string]any
	signed []byte
	sig    []byte
}

func parseToken(raw string) (*token, error) {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	if len(parts) != 3 {
		return nil, errors.New("not a compact JWS")
	}
	hdr, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hdr, &header); err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(payload)))
	dec.UseNumber()
	claims := map[string]any{}
	if err := dec.Decode(&claims); err != nil {
		return nil, fmt.Errorf("claims: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}
	return &token{alg: header.Alg, kid: header.Kid, claims: claims, signed: []byte(parts[0] + "." + parts[1]), sig: sig}, nil
}

// Checks the signature against the key set, or the client secret for HMAC algorithms
func (t *token) verify(keys []jwk, secret string) error {
	if t.alg == "" || t.alg == "none" {
		return errors.New("unsigned token")
	}
	if strings.HasPrefix(t.alg, "HS") {
		h, ok := hashes[t.alg]
		if !ok {
			return fmt.Errorf("unsupported algorithm %q", t.alg)
		}
		if secret == "" {
			return errors.New("the token is signed with the client secret but none is configured")
		}
		mac := hmac.New(h.New, []byte(secret))
		mac.Write(t.signed)
		if !hmac.Equal(mac.Sum(nil), t.sig) {
			return errBadSignature
		}
		return nil
	}
	kty := keyType(t.alg)
	if kty == "" {
		return fmt.Errorf("unsupported algorithm %q", t.alg)
	}
	tried := 0
	for _, k := range keys {
		if k.Kty != kty || (t.kid != "" && k.Kid != t.kid) || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != t.alg) {
			continue
		}
		pub, err := k.public()
		if err != nil {
			continue
		}
		tried++
		if verifyWith(t.alg, pub, t.signed, t.sig) {
			return nil
		}
	}
	if tried == 0 {
		return errUnknownKey
	}
	return errBadSignature
}

func verifyWith(alg string, pub crypto.PublicKey, signed, sig []byte) bool {
	if alg == "EdDSA" {
		key, ok := pub.(ed25519.PublicKey)
		return ok && ed25519.Verify(key, signed, sig)
	}
	h, ok := hashes[alg]
	if !ok {
		return false
	}
	digest := h.New()
	digest.Write(signed)
	sum := digest.Sum(nil)
	switch alg[:2] {
	case "RS":
		key, ok := pub.(*rsa.PublicKey)
		return ok && rsa.VerifyPKCS1v15(key, h, sum, sig) == nil
	case "PS":
		key, ok := pub.(*rsa.PublicKey)
		return ok && rsa.VerifyPSS(key, h, sum, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) == nil
	case "ES":
		key, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return false
		}
		size := (key.Curve.Params().BitSize + 7) / 8
		if len(sig) != 2*size {
			return false
		}
		r, s := new(big.Int).SetBytes(sig[:size]), new(big.Int).SetBytes(sig[size:])
		return ecdsa.Verify(key, sum, r, s)
	}
	return false
}

// Looks a claim up by its full name, then as a dotted path into nested objects
func claimLookup(claims map[string]any, path string) (any, bool) {
	if v, ok := claims[path]; ok {
		return v, true
	}
	var cur any = claims
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func claimString(claims map[string]any, key string) string {
	s, _ := claims[key].(string)
	return s
}

func claimBool(claims map[string]any, key string) (value, present bool) {
	switch v := claims[key].(type) {
	case bool:
		return v, true
	case string:
		// Some providers send the flag as text.
		return strings.EqualFold(v, "true"), true
	}
	return false, false
}

func claimTime(claims map[string]any, key string) (time.Time, bool) {
	var secs float64
	switch v := claims[key].(type) {
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return time.Time{}, false
		}
		secs = f
	case float64:
		secs = v
	default:
		return time.Time{}, false
	}
	return time.Unix(int64(secs), 0), true
}

// Reads a list claim: an array of strings, one string, or an object whose keys name the entries
func claimStrings(v any) []string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case map[string]any:
		var out []string
		for k := range x {
			out = append(out, k)
		}
		return out
	}
	return nil
}
