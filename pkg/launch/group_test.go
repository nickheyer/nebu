package launch

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Launches one shell per script, each writing its own log file in one directory
func ranks(t *testing.T, scripts ...string) []Handle {
	t.Helper()
	dir := t.TempDir()
	var out []Handle
	for i, script := range scripts {
		h, err := launcher().Launch(context.Background(), Spec{Command: "sh", Args: []string{"-c", script}, LogPath: filepath.Join(dir, "rank"+string(rune('0'+i))+".log")})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h)
	}
	return out
}

// A group merges rank output, stops the rest when one rank exits, names it, and stops all on request
func TestGroup(t *testing.T) {
	cases := []struct {
		name    string
		scripts []string
		stop    bool
		err     string
		lines   []string
	}{
		{name: "a crashed rank stops the others", scripts: []string{"echo zero; sleep 30", "echo one; sleep 0.3; exit 3"}, err: "rank 1: exit status 3", lines: []string{"zero", "one"}},
		{name: "a clean exit still ends the group", scripts: []string{"echo zero; sleep 0.3", "echo one; sleep 30"}, err: "rank 0 exited with status 0", lines: []string{"zero", "one"}},
		{name: "a stop ends every rank", scripts: []string{"echo zero; sleep 30", "echo one; sleep 30", "echo two; sleep 30"}, stop: true, err: "rank", lines: []string{"zero", "one", "two"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			members := ranks(t, c.scripts...)
			g := NewGroup(members, time.Second)
			if g.Pid() != members[0].Pid() || len(g.Members()) != len(c.scripts) {
				t.Fatalf("pid %d members %d", g.Pid(), len(g.Members()))
			}
			waitLines(t, g.Log(), len(c.lines))
			if c.stop {
				if err := g.Stop(context.Background(), time.Second); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-g.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("the group never ended")
			}
			for i, m := range members {
				select {
				case <-m.Done():
				default:
					t.Fatalf("rank %d still runs after the group ended", i)
				}
			}
			if err := g.Err(); err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("err %v, want %q", err, c.err)
			}
			got := g.Log().Tail(0)
			for _, want := range c.lines {
				if !slices.Contains(got, want) {
					t.Fatalf("merged log %v lacks %q", got, want)
				}
			}
		})
	}
}

// A tee copies the lines a log already holds and every later one
func TestTee(t *testing.T) {
	from, to := NewLog(10), NewLog(10)
	from.Write("before")
	from.Tee(to)
	from.Write("after")
	if got := to.Tail(0); !slices.Equal(got, []string{"before", "after"}) {
		t.Fatalf("tee holds %v", got)
	}
}
