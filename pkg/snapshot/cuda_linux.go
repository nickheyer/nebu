//go:build linux

package snapshot

import (
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/nickheyer/nebu/pkg/proc"
)

// Pids of a tree holding an NVIDIA device open, the ones whose CUDA state is parked before a dump, sorted
func cudaPIDs(root int) ([]int, error) {
	family, err := proc.Snapshot(root)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, pid := range family.PIDs() {
		entries, err := os.ReadDir("/proc/" + strconv.Itoa(pid) + "/fd")
		if err != nil {
			continue
		}
		for _, e := range entries {
			target, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/fd/" + e.Name())
			if err == nil && strings.HasPrefix(target, "/dev/nvidia") {
				out = append(out, pid)
				break
			}
		}
	}
	sort.Ints(out)
	return out, nil
}

// Kills a restored tree whose CUDA state would not come back, reaping its root and the workers it left
func discard(pid int) error {
	family, err := proc.Snapshot(pid)
	syscall.Kill(pid, syscall.SIGKILL)
	var status syscall.WaitStatus
	for {
		if _, werr := syscall.Wait4(pid, &status, 0, nil); !errors.Is(werr, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return err
	}
	_, err = family.KillSurvivors()
	return err
}
