//go:build !unix

package host

import (
	"errors"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Reports storage as unsupported on this platform
func stat(path string) (*v1.Storage, error) {
	return nil, errors.ErrUnsupported
}
