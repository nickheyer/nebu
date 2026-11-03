package main

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/stub"
)

func TestParse(t *testing.T) {
	o, err := parse([]string{
		"-listen", "127.0.0.1:9000", "-id", "s1", "-model", "m", "-adapters", "a=/p/a, b,", "-names", "llamacpp",
		"-max-num-seqs", "8", "-num-gpu-blocks", "64", "-block-size", "32", "-base-latency", "5ms", "-prefill-per-token", "1ms",
		"-decode-per-token", "7ms", "-contention", "0.5", "-mode", "ratio", "-speedup", "20", "-max-loras", "2",
		"-lora-load-delay", "1s", "-load-time", "2s", "-default-max-tokens", "9", "-records", "5", "-drain", "3s",
	})
	if err != nil {
		t.Fatal(err)
	}
	c := o.cfg
	if o.listen != "127.0.0.1:9000" || o.drain != 3*time.Second || c.ID != "s1" || c.Model != "m" || c.Names != stub.NamesLlamaCpp ||
		c.MaxNumSeqs != 8 || c.NumGPUBlocks != 64 || c.BlockSize != 32 || c.BaseLatency != 5*time.Millisecond ||
		c.PrefillPerToken != time.Millisecond || c.DecodePerToken != 7*time.Millisecond || c.Contention != 0.5 ||
		c.Mode != stub.ModeRatio || c.Speedup != 20 || c.MaxLoras != 2 || c.LoraLoadDelay != time.Second ||
		c.LoadTime != 2*time.Second || c.DefaultMaxTokens != 9 || c.Records != 5 {
		t.Fatalf("parsed %+v", o)
	}
	if want := []stub.Adapter{{Name: "a", Path: "/p/a"}, {Name: "b", Path: "b"}}; !slices.Equal(c.Adapters, want) {
		t.Fatalf("adapters %+v", c.Adapters)
	}
	if _, err := parse([]string{"extra"}); err == nil {
		t.Fatal("positional argument accepted")
	}
	if err := run([]string{"-names", "tgi", "-listen", "127.0.0.1:0"}, io.Discard); err == nil {
		t.Fatal("unknown metric names accepted")
	}
}

func TestRunServesUntilSIGTERM(t *testing.T) {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- run([]string{"-listen", "127.0.0.1:0", "-id", "s1", "-names", "sglang", "-mode", "ratio", "-speedup", "100"}, pw)
	}()
	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + strings.TrimSpace(line)
	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Stub-Id") != "s1" || resp.Header.Get("X-Served-By") != strings.TrimSpace(line) {
		t.Fatalf("health %d headers %v", resp.StatusCode, resp.Header)
	}
	resp, err = http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "sglang:num_queue_reqs") {
		t.Fatalf("metrics %s", body)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("still serving after SIGTERM")
	}
}
