package auth

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Bytes of entropy in a generated API token
const tokenBytes = 32

// Reads the API token file, creating a random one with mode 0600 when it is missing
func LoadOrCreateToken(path string) (token string, created bool, err error) {
	data, err := os.ReadFile(path)
	if err == nil {
		token = strings.TrimSpace(string(data))
		if token == "" {
			return "", false, fmt.Errorf("api token file %s is empty, delete it to make a new one", path)
		}
		return token, false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", false, fmt.Errorf("api token: %w", err)
	}
	if token, err = RandomString(tokenBytes); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", false, fmt.Errorf("api token: %w", err)
	}
	return token, true, nil
}
