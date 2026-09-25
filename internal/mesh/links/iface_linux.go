package links

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The interface's reported speed in bits per second, from the kernel's class tree. Virtual
// interfaces report none.
func speedOf(name string) uint64 {
	data, err := os.ReadFile(filepath.Join("/sys/class/net", name, "speed"))
	if err != nil {
		return 0
	}
	mbps, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || mbps <= 0 {
		return 0
	}
	return uint64(mbps) * 1e6
}

// The RDMA device bound to the interface, from the kernel's class tree: the device's own
// infiniband entry, or the infiniband device whose net entry names the interface
func rdmaOf(name string) string {
	if entries, err := os.ReadDir(filepath.Join("/sys/class/net", name, "device", "infiniband")); err == nil && len(entries) > 0 {
		return entries[0].Name()
	}
	devices, err := os.ReadDir("/sys/class/infiniband")
	if err != nil {
		return ""
	}
	for _, d := range devices {
		if _, err := os.Stat(filepath.Join("/sys/class/infiniband", d.Name(), "device", "net", name)); err == nil {
			return d.Name()
		}
	}
	return ""
}
