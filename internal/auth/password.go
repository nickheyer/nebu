package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id settings, the OWASP recommendation for interactive logins
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	saltBytes    = 16
	hashBytes    = 32
	MinPassword  = 8
	MaxPassword  = 1024
)

// Checks the password policy
func checkPassword(pw string) error {
	if len(pw) < MinPassword {
		return fmt.Errorf("%w: password needs at least %d characters", ErrUser, MinPassword)
	}
	if len(pw) > MaxPassword {
		return fmt.Errorf("%w: password is longer than %d bytes", ErrUser, MaxPassword)
	}
	return nil
}

// Hashes a password in PHC form: $argon2id$v=19$m=...,t=...,p=...$salt$hash
func HashPassword(pw string) (string, error) {
	if err := checkPassword(pw); err != nil {
		return "", err
	}
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, hashBytes)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(sum)), nil
}

// Checks a password against a PHC hash, in constant time over the hash
func VerifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var errNoHash = errors.New("no password hash")
