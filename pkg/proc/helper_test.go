package proc

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Names the role a re-executed test binary plays
const helperEnv = "PROC_TEST_HELPER"

// Roles a re-executed test binary plays instead of running tests
var helpers = map[string]func(){
	"sleep":        func() { time.Sleep(time.Minute) },
	"listen":       listenHelper,
	"listen-child": func() { helperChild("listen") },
}

// Listens on a loopback port, prints it, and waits
func listenHelper() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	fmt.Println(ln.Addr().(*net.TCPAddr).Port)
	time.Sleep(time.Minute)
}

// Runs another helper role as a child, relaying its output, and waits for it
func helperChild(role string) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"="+role)
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		os.Exit(2)
	}
}

func TestMain(m *testing.M) {
	if role, ok := helpers[os.Getenv(helperEnv)]; ok {
		role()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A command re-running this test binary in a helper role, in a group of its own
func helper(role string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"="+role)
	cmd.SysProcAttr = Attr()
	return cmd
}
