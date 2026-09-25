package links

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// The interface's reported speed in bits per second, from the adapter's Speed property
func speedOf(name string) uint64 {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	script := "(Get-NetAdapter -Name '" + strings.ReplaceAll(name, "'", "''") + "' -ErrorAction Stop).Speed"
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// The RDMA transports nebu drives bind through the Linux class tree, so a link from a Windows
// node is sockets and the class reflects that
func rdmaOf(string) string { return "" }
