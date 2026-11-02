//go:build linux

package proc

import (
	"os"
	"strconv"
	"strings"
)

// Reads every process and its parent from the stat file of each proc entry
func parentsOf() (map[int]int, error) {
	return parentsIn("/proc")
}

// The parent of every process under a proc mount, the comm field skipped past its closing paren
func parentsIn(root string) (map[int]int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := map[int]int{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(root + "/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		stat := string(data)
		end := strings.LastIndexByte(stat, ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(stat[end+1:])
		if len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		out[pid] = ppid
	}
	return out, nil
}
