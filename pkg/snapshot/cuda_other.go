//go:build !linux

package snapshot

import "errors"

// CRIU and cuda-checkpoint run on Linux alone, so no tree holds CUDA state a dump could park elsewhere
func cudaPIDs(int) ([]int, error) {
	return nil, errors.New("process snapshots run on linux alone")
}

// No tree is ever restored elsewhere, so none can be killed
func discard(int) error {
	return errors.New("process snapshots run on linux alone")
}
