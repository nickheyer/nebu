//go:build linux

package proc

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func init() {
	helpers["detach"] = func() {
		child := exec.Command(os.Args[0], "-test.run=^$")
		child.Env = append(os.Environ(), helperEnv+"=sleep")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			fmt.Println("error", err)
			return
		}
		fmt.Println(child.Process.Pid)
		time.Sleep(time.Minute)
	}
}

// A grandchild in a session of its own escapes the group kill and dies by the snapshot taken before
func TestKillSurvivorsReachesADetachedGrandchild(t *testing.T) {
	root := helper("detach")
	out, err := root.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("helper said %q", line)
	}
	defer syscall.Kill(grandchild, syscall.SIGKILL)
	family, err := Snapshot(root.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(family.pids(), grandchild) {
		t.Fatalf("snapshot %v lacks the grandchild %d", family.pids(), grandchild)
	}
	syscall.Kill(-root.Process.Pid, syscall.SIGKILL)
	root.Wait()
	if !Exists(grandchild) {
		t.Fatal("the detached grandchild should outlive the group kill")
	}
	killed, err := family.KillSurvivors()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(killed, grandchild) {
		t.Fatalf("killed %v, want the grandchild %d", killed, grandchild)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WaitExit(ctx, grandchild); err != nil {
		t.Fatalf("grandchild outlived the kill: %v", err)
	}
}

// A recorded start time that no longer matches leaves the process alone
func TestKillSurvivorsSparesAReusedPid(t *testing.T) {
	cmd := helper("sleep")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	id, alive, err := identity(cmd.Process.Pid)
	if err != nil || !alive || id == "" {
		t.Fatalf("identity %q %v %v", id, alive, err)
	}
	family := &Family{members: map[int]string{cmd.Process.Pid: id + "0"}}
	killed, err := family.killMembers()
	if err != nil || len(killed) != 0 {
		t.Fatalf("killed %v %v", killed, err)
	}
	if !Exists(cmd.Process.Pid) {
		t.Fatal("a pid under another start time was signalled")
	}
	family.members[cmd.Process.Pid] = id
	if killed, err := family.killMembers(); err != nil || !slices.Equal(killed, []int{cmd.Process.Pid}) {
		t.Fatalf("matching start time: %v %v", killed, err)
	}
	if _, alive, _ := identity(os.Getpid()); !alive {
		t.Fatal("the test process reads as alive")
	}
}
