package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/launch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Every line the logger writes is kept in the ring behind it, the file still getting them all
func TestNewKeepsRecentLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nebu.log")
	log, recent, closer, err := New(&v1.Logging{File: path, Level: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	log.Info("listening", "addr", "127.0.0.1:8484")
	log.Debug("rpc", "procedure", "/nebu.v1.HostService/GetProfile")
	closer.Close()
	lines := recent.Tail(0)
	if len(lines) != 2 || !strings.Contains(lines[0], "msg=listening") || !strings.Contains(lines[1], "procedure=/nebu.v1.HostService/GetProfile") {
		t.Fatalf("%q", lines)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "\n") != 2 || !strings.Contains(string(data), "msg=listening") {
		t.Fatalf("%q", data)
	}
}

// A line under the configured level reaches neither the output nor the ring
func TestNewKeepsOnlyEnabledLevels(t *testing.T) {
	log, recent, closer, err := New(&v1.Logging{File: filepath.Join(t.TempDir(), "nebu.log"), Level: "warn"})
	if err != nil {
		t.Fatal(err)
	}
	log.Info("quiet")
	log.Warn("loud")
	closer.Close()
	if lines := recent.Tail(0); len(lines) != 1 || !strings.Contains(lines[0], "msg=loud") {
		t.Fatalf("%q", lines)
	}
}

// Output arriving in pieces still lands in the ring one whole line at a time
func TestLineWriterSplitsOnNewlines(t *testing.T) {
	ring := launch.NewLog(10)
	w := &lineWriter{ring: ring}
	w.Write([]byte("one\ntw"))
	w.Write([]byte("o\nthree"))
	if got := ring.Tail(0); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("%q", got)
	}
	w.Write([]byte("\n"))
	if got := ring.Tail(0); len(got) != 3 || got[2] != "three" {
		t.Fatalf("%q", got)
	}
}
