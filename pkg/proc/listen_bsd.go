//go:build unix && !linux

package proc

import (
	"os/exec"
	"strconv"
	"strings"
)

// TCP ports one process listens on, read through lsof in field output
func listeningPorts(pid int) ([]int, error) {
	out, err := exec.Command("lsof", "-nP", "-a", "-p", strconv.Itoa(pid), "-iTCP", "-sTCP:LISTEN", "-Fn").Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 && len(out) == 0 {
			return nil, nil
		}
		return nil, err
	}
	var ports []int
	for _, line := range strings.Split(string(out), "\n") {
		name, ok := strings.CutPrefix(line, "n")
		if !ok {
			continue
		}
		port, err := strconv.Atoi(name[strings.LastIndex(name, ":")+1:])
		if err != nil {
			continue
		}
		ports = append(ports, port)
	}
	return ports, nil
}
