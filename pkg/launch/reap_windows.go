//go:build windows

package launch

import "errors"

// CRIU restores processes on Linux alone, so no restored child exists here to reap
func Reap(int, string) (Handle, error) {
	return nil, errors.New("restored processes are reaped on unix alone")
}
