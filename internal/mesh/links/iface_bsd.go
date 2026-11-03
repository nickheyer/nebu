//go:build darwin || freebsd || openbsd || netbsd || dragonfly

package links

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The rate in the media line ifconfig prints, such as 10Gbase-T
var mediaRate = regexp.MustCompile(`(\d+)(G|M)?base`)

// The interface's reported speed in bits per second, from the media line ifconfig prints for it
func speedOf(name string) uint64 {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ifconfig", name).Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "media:") {
			continue
		}
		m := mediaRate.FindStringSubmatch(line)
		if m == nil {
			return 0
		}
		n, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			return 0
		}
		switch m[2] {
		case "G":
			return n * 1e9
		case "M", "":
			return n * 1e6
		}
	}
	return 0
}

// No RDMA device binds here since the transports nebu drives use the Linux class tree
func rdmaOf(string) string { return "" }
