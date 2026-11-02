package instances

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/proc"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Processes under a live instance's pid carry its id and name, a stopped instance's and a stranger's carry none
func TestAttributeStampsProcessesOfLiveInstances(t *testing.T) {
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
		for pid, root := range proc.Owners([]int{cmd.Process.Pid}) {
			if pid != root {
				grandchild = pid
			}
		}
	}
	if grandchild == 0 {
		t.Fatal("no grandchild appeared")
	}
	m := &Manager{}
	m.list = []*instance{
		{mgr: m, rec: &v1.Instance{Id: "in1", Name: "model", Pid: int32(cmd.Process.Pid), State: v1.InstanceState_INSTANCE_STATE_READY}},
		{mgr: m, rec: &v1.Instance{Id: "old", Name: "gone", Pid: int32(os.Getpid()), State: v1.InstanceState_INSTANCE_STATE_STOPPED}},
	}
	profile := &v1.HostProfile{Processes: []*v1.ProcessUse{
		{DeviceId: "gpu", Pid: int32(cmd.Process.Pid), Bytes: 10},
		{DeviceId: "gpu", Pid: int32(grandchild), Bytes: 20},
		{DeviceId: "gpu", Pid: int32(os.Getpid()), Bytes: 30},
	}}
	m.Attribute(profile)
	for i, want := range []string{"in1", "in1", ""} {
		if got := profile.Processes[i].GetInstanceId(); got != want {
			t.Fatalf("process %d instance %q, want %q", i, got, want)
		}
	}
	if profile.Processes[1].GetInstanceName() != "model" || profile.Processes[2].GetInstanceName() != "" {
		t.Fatalf("names %v", profile.Processes)
	}
	m.Attribute(&v1.HostProfile{})
}
