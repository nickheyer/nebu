//go:build windows

package proc

import (
	"os/exec"
	"strconv"
	"strings"
)

// TCP ports one process listens on, read from the netstat table rows ending in its pid
func listeningPorts(pid int) ([]int, error) {
	out, err := exec.Command("netstat", "-ano", "-p", "TCP").Output()
	if err != nil {
		return nil, err
	}
	var ports []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "TCP" || fields[3] != "LISTENING" || fields[4] != strconv.Itoa(pid) {
			continue
		}
		port, err := strconv.Atoi(fields[1][strings.LastIndex(fields[1], ":")+1:])
		if err != nil {
			continue
		}
		ports = append(ports, port)
	}
	return ports, nil
}
