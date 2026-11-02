package proc

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// A child and its grandchild belong to the root they descend from, the root to itself, strangers to no one
func TestOwnersFollowTheTreeToItsRoot(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	cmd := exec.Command(sh, "-c", "sleep 30 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	var grandchild int
	for deadline := time.Now().Add(5 * time.Second); grandchild == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		parents, err := parentsOf()
		if err != nil {
			t.Fatal(err)
		}
		for pid, ppid := range parents {
			if ppid == cmd.Process.Pid {
				grandchild = pid
			}
		}
	}
	if grandchild == 0 {
		t.Fatalf("no child of %d appeared", cmd.Process.Pid)
	}
	owners := Owners([]int{cmd.Process.Pid})
	if owners[cmd.Process.Pid] != cmd.Process.Pid || owners[grandchild] != cmd.Process.Pid {
		t.Fatalf("root %d child %d owners %d %d", cmd.Process.Pid, grandchild, owners[cmd.Process.Pid], owners[grandchild])
	}
	if _, owned := owners[os.Getpid()]; owned {
		t.Fatal("the test process belongs to no root")
	}
	if Owners(nil) != nil {
		t.Fatal("no roots own nothing")
	}
}
