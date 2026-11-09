//go:build linux

package instances

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/proc"
)

// The kernel's local port range, what it hands to sockets bound to port zero
func ephemeralRange() (proc.Range, error) {
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return proc.Range{}, fmt.Errorf("ephemeral port range: %w", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return proc.Range{}, fmt.Errorf("ephemeral port range %q is not two ports", strings.TrimSpace(string(raw)))
	}
	low, err := strconv.Atoi(fields[0])
	if err != nil {
		return proc.Range{}, fmt.Errorf("ephemeral port range: %w", err)
	}
	high, err := strconv.Atoi(fields[1])
	if err != nil {
		return proc.Range{}, fmt.Errorf("ephemeral port range: %w", err)
	}
	return proc.Range{Low: low, High: high}, nil
}
