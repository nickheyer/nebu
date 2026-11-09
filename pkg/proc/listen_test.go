package proc

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A child's listening port is read from the socket table, a port taken by a grandchild included, none once gone
func TestListeningPortsReadTheSocketTable(t *testing.T) {
	cases := []struct {
		name string
		role string
	}{
		{"the child itself", "listen"},
		{"a grandchild", "listen-child"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := helper(tc.role)
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			tree := Adopt(cmd)
			defer func() {
				tree.Kill()
				cmd.Wait()
				tree.Close()
			}()
			line, err := bufio.NewReader(out).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			want, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				ports, err := ListeningPorts(cmd.Process.Pid)
				if err != nil {
					t.Fatal(err)
				}
				if len(ports) == 1 && ports[0] == want {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("ports %v, want %d", ports, want)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
	if _, err := ListeningPorts(os.Getpid() + 1_000_000); err == nil {
		t.Fatal("a pid that is gone is an error, not an empty listing")
	}
}
