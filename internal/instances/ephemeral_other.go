//go:build !linux

package instances

import "github.com/nickheyer/nebu/pkg/proc"

// The IANA dynamic range Windows, macOS and the BSDs hand to sockets bound to port zero
func ephemeralRange() (proc.Range, error) {
	return proc.Range{Low: 49152, High: 65535}, nil
}
