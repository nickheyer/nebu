//go:build linux

package proc

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// The TCP listen state in the proc net tables
const tcpListen = "0A"

// TCP ports one process listens on, its socket inodes matched against the tcp and tcp6 tables
func listeningPorts(pid int) ([]int, error) {
	return listeningIn("/proc", pid)
}

// Listening ports of a pid under a proc mount, a process gone mid read listening on nothing
func listeningIn(root string, pid int) ([]int, error) {
	dir := root + "/" + strconv.Itoa(pid)
	entries, err := os.ReadDir(dir + "/fd")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	inodes := map[string]bool{}
	for _, e := range entries {
		target, err := os.Readlink(dir + "/fd/" + e.Name())
		if err != nil {
			continue
		}
		if inode, ok := strings.CutPrefix(target, "socket:["); ok {
			inodes[strings.TrimSuffix(inode, "]")] = true
		}
	}
	if len(inodes) == 0 {
		return nil, nil
	}
	var out []int
	for _, table := range []string{"/net/tcp", "/net/tcp6"} {
		ports, err := listeningInTable(dir+table, inodes)
		if err != nil {
			return nil, err
		}
		out = append(out, ports...)
	}
	return out, nil
}

// Ports of the listening rows of one tcp table whose inode the process holds
func listeningInTable(path string, inodes map[string]bool) ([]int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []int
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[3] != tcpListen || !inodes[fields[9]] {
			continue
		}
		_, hexPort, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseInt(hexPort, 16, 32)
		if err != nil {
			continue
		}
		out = append(out, int(port))
	}
	return out, scanner.Err()
}
