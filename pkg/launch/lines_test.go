package launch

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// Progress bars split at each CR, CRLF breaks once even across reads, and blank LF lines stay
func TestLineSplitter(t *testing.T) {
	input := "start\r 10%\r 50%\r100%\r\ndone\r\n\r\nafter blank\nlast"
	want := []string{"start", " 10%", " 50%", "100%", "done", "", "after blank", "last"}
	for _, chunk := range []int{1, 2, 3, 7, len(input)} {
		var got []string
		if err := Lines(&chunked{data: []byte(input), size: chunk}, func(line string) { got = append(got, line) }); err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("chunk %d: %q, want %q", chunk, got, want)
		}
	}
	var stream []string
	s := newLineStream()
	for _, part := range []string{"a\r", "\nb\r", "c\n", "\r\r", "\n", "tail"} {
		s.feed([]byte(part), func(line string) { stream = append(stream, line) })
	}
	s.flush(func(line string) { stream = append(stream, line) })
	if strings.Join(stream, "|") != "a|b|c||tail" {
		t.Fatalf("stream %q", stream)
	}
}

// A reader handing out a fixed number of bytes per read
type chunked struct {
	data []byte
	size int
}

func (c *chunked) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.data[:min(c.size, len(c.data))])
	c.data = c.data[n:]
	return n, nil
}

// A line past one MiB without a newline arrives whole, and one past MaxLine arrives in MaxLine pieces
func TestLinesLongLines(t *testing.T) {
	long := strings.Repeat("x", 3<<20)
	var got []string
	if err := Lines(strings.NewReader("a\n"+long), func(line string) { got = append(got, line) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != long {
		t.Fatalf("got %d lines, the last %d bytes", len(got), len(got[len(got)-1]))
	}
	huge := strings.Repeat("y", MaxLine+5)
	var sizes []int
	if err := Lines(strings.NewReader(huge+"\nz"), func(line string) { sizes = append(sizes, len(line)) }); err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 3 || sizes[0] != MaxLine || sizes[1] != 5 || sizes[2] != 1 {
		t.Fatalf("pieces %v", sizes)
	}
}

// Appended bytes reach the log on the file's notification, well inside the old 100ms poll
func TestTailFollowsWritesAsTheyHappen(t *testing.T) {
	path := logPath(t)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	log := NewLog(100)
	tail, err := newTailer(path, log)
	if err != nil {
		t.Fatal(err)
	}
	exited, done := make(chan struct{}), make(chan struct{})
	go tail.follow(exited, done)
	updates := make(chan struct{}, 16)
	follow, stop := context.WithCancel(context.Background())
	defer stop()
	go log.Follow(follow, 0, func([]string) error {
		updates <- struct{}{}
		return nil
	})
	for i, write := range []string{"first\n", "progress 1\rprogress 2\r", "second\r\n"} {
		start := time.Now()
		if _, err := f.WriteString(write); err != nil {
			t.Fatal(err)
		}
		select {
		case <-updates:
		case <-time.After(5 * time.Second):
			t.Fatalf("write %d never reached the log", i)
		}
		if waited := time.Since(start); waited > 90*time.Millisecond {
			t.Fatalf("write %d took %v to reach the log", i, waited)
		}
		for len(updates) > 0 {
			<-updates
		}
	}
	f.WriteString("unterminated")
	close(exited)
	<-done
	if got := strings.Join(log.Tail(0), "|"); got != "first|progress 1|progress 2|second|unterminated" {
		t.Fatalf("log %q", got)
	}
}

// Stop with a context already ended returns its error at once, with the process killed
func TestStopHonorsAnEndedContext(t *testing.T) {
	p, err := launcher().Launch(context.Background(), Spec{Command: "sh", Args: []string{"-c", `trap "" TERM; sleep 30`}, LogPath: logPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := p.Stop(ctx, time.Minute); err != context.Canceled {
		t.Fatalf("stop with an ended context: %v", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("stop took %v", waited)
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("an ended context kills the process instead of waiting out the grace")
	}
	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(deadline, time.Minute); err != nil {
		t.Fatalf("stop after exit: %v", err)
	}
}
