package sso

import (
	"crypto/hmac"
	"crypto/sha256"
	"hash"
)

func newHMAC(secret string) hash.Hash { return hmac.New(sha256.New, []byte(secret)) }
