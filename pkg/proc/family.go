package proc

import (
	"errors"
	"fmt"
	"sort"
)

// A process and its descendants as one snapshot saw them, each tied to its start time against pid reuse
type Family struct {
	root    int
	rootID  string
	members map[int]string
}

// Records pid and every descendant alive now with the start time that identifies each
func Snapshot(pid int) (*Family, error) {
	parents, err := parentsOf()
	if err != nil {
		return nil, fmt.Errorf("snapshot of pid %d: %w", pid, err)
	}
	children := map[int][]int{}
	for child, parent := range parents {
		children[parent] = append(children[parent], child)
	}
	rootID, _, err := identity(pid)
	if err != nil {
		return nil, fmt.Errorf("snapshot of pid %d: %w", pid, err)
	}
	f := &Family{root: pid, rootID: rootID, members: map[int]string{}}
	seen := map[int]bool{pid: true}
	type link struct {
		pid      int
		parentID string
	}
	var queue []link
	for _, c := range children[pid] {
		queue = append(queue, link{c, rootID})
	}
	// Walks down the tree, following the children of a pid that exited since the tree was read
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if seen[next.pid] {
			continue
		}
		seen[next.pid] = true
		id, alive, err := identity(next.pid)
		if err != nil {
			return nil, fmt.Errorf("snapshot of pid %d: %w", pid, err)
		}
		if alive && !descends(id, next.parentID) {
			continue
		}
		if alive {
			f.members[next.pid] = id
		}
		for _, c := range children[next.pid] {
			queue = append(queue, link{c, id})
		}
	}
	return f, nil
}

// The root and every recorded descendant in ascending order
func (f *Family) PIDs() []int {
	return append([]int{f.root}, f.pids()...)
}

// Pids of the recorded descendants in ascending order
func (f *Family) pids() []int {
	out := make([]int, 0, len(f.members))
	for pid := range f.members {
		out = append(out, pid)
	}
	sort.Ints(out)
	return out
}

// Kills every recorded descendant still alive under its recorded start time, returning the pids killed
func (f *Family) killMembers() ([]int, error) {
	var killed []int
	var errs []error
	for _, pid := range f.pids() {
		ok, err := killSame(pid, f.members[pid])
		if err != nil {
			errs = append(errs, fmt.Errorf("kill pid %d: %w", pid, err))
			continue
		}
		if ok {
			killed = append(killed, pid)
		}
	}
	return killed, errors.Join(errs...)
}
