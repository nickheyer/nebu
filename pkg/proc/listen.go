package proc

import (
	"fmt"
	"sort"
)

// TCP ports pid and its descendants listen on, read from the kernel's socket table, sorted
func ListeningPorts(pid int) ([]int, error) {
	parents, err := parentsOf()
	if err != nil {
		return nil, err
	}
	if _, ok := parents[pid]; !ok {
		return nil, fmt.Errorf("pid %d is gone", pid)
	}
	members := []int{pid}
	seen := map[int]bool{pid: true}
	for i := 0; i < len(members); i++ {
		for child, parent := range parents {
			if parent == members[i] && !seen[child] {
				seen[child] = true
				members = append(members, child)
			}
		}
	}
	found := map[int]bool{}
	for _, p := range members {
		ports, err := listeningPorts(p)
		if err != nil {
			return nil, err
		}
		for _, port := range ports {
			found[port] = true
		}
	}
	out := make([]int, 0, len(found))
	for port := range found {
		out = append(out, port)
	}
	sort.Ints(out)
	return out, nil
}
