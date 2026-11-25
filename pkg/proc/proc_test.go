package proc

import "testing"

// A child runs its command from the moment its start returns, however far its exec has come
func TestRunningFromStart(t *testing.T) {
	for range 50 {
		cmd := helper("sleep")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		tree := Adopt(cmd)
		running := Running(cmd.Process.Pid, cmd.Args)
		tree.Kill()
		cmd.Wait()
		tree.Close()
		if !running {
			t.Fatal("a child just started runs its command")
		}
	}
}
