// Command nebu-stub serves an emulated LLM inference server for scheduler tests
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nickheyer/nebu/pkg/stub"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "nebu-stub:", err)
		os.Exit(1)
	}
}

// Settings parsed from the command line
type options struct {
	listen string
	drain  time.Duration
	cfg    stub.Config
}

// Parses flags into stub settings, the listen address and the shutdown drain time
func parse(args []string) (options, error) {
	var o options
	var adapters, names, mode string
	fs := flag.NewFlagSet("nebu-stub", flag.ContinueOnError)
	fs.StringVar(&o.listen, "listen", ":8000", "address to listen on")
	fs.DurationVar(&o.drain, "drain", 30*time.Second, "time in-flight requests get to finish after SIGTERM")
	fs.StringVar(&o.cfg.ID, "id", "stub", "value of the X-Stub-Id header")
	fs.StringVar(&o.cfg.Model, "model", "stub-model", "base model name")
	fs.StringVar(&adapters, "adapters", "", "comma separated adapters resident at start, each name or name=path")
	fs.StringVar(&names, "names", string(stub.NamesVLLM), "metric names, vllm, sglang or llamacpp")
	fs.IntVar(&o.cfg.MaxNumSeqs, "max-num-seqs", 256, "running sequence cap")
	fs.IntVar(&o.cfg.NumGPUBlocks, "num-gpu-blocks", 4096, "KV cache size in blocks")
	fs.IntVar(&o.cfg.BlockSize, "block-size", 16, "tokens per KV block")
	fs.IntVar(&o.cfg.MaxLoras, "max-loras", 4, "adapter slots")
	fs.DurationVar(&o.cfg.LoraLoadDelay, "lora-load-delay", 0, "emulated adapter load time")
	fs.DurationVar(&o.cfg.LoadTime, "load-time", 0, "wall time health and model endpoints answer 503 after start")
	fs.DurationVar(&o.cfg.BaseLatency, "base-latency", 15*time.Millisecond, "emulated fixed prefill cost")
	fs.DurationVar(&o.cfg.PrefillPerToken, "prefill-per-token", 100*time.Microsecond, "emulated prefill cost per uncached prompt token")
	fs.DurationVar(&o.cfg.DecodePerToken, "decode-per-token", 15*time.Millisecond, "emulated cost per decoded token")
	fs.Float64Var(&o.cfg.Contention, "contention", 0.02, "step slowdown per other running sequence")
	fs.StringVar(&mode, "mode", string(stub.ModeTime), "time sleeps emulated durations, ratio divides them by speedup")
	fs.Float64Var(&o.cfg.Speedup, "speedup", 1, "wall time divisor in ratio mode")
	fs.IntVar(&o.cfg.DefaultMaxTokens, "default-max-tokens", 16, "output words when a request sets no max_tokens")
	fs.IntVar(&o.cfg.Records, "records", 100000, "per request records kept for /stub/stats, zero keeping all")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	if o.drain < 0 {
		return o, errors.New("drain must not be negative")
	}
	o.cfg.Names, o.cfg.Mode = stub.Names(names), stub.Mode(mode)
	for _, item := range strings.Split(adapters, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, path, ok := strings.Cut(item, "=")
		if !ok {
			path = name
		}
		o.cfg.Adapters = append(o.cfg.Adapters, stub.Adapter{Name: name, Path: path})
	}
	return o, nil
}

// Serves the stub, prints the bound address, and drains in-flight requests on SIGTERM or interrupt
func run(args []string, out io.Writer) error {
	o, err := parse(args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	st, err := stub.New(o.cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	ln, err := net.Listen("tcp", o.listen)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, ln.Addr().String())
	srv := &http.Server{Handler: st, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	drain, cancel := context.WithTimeout(context.Background(), o.drain)
	defer cancel()
	if err := srv.Shutdown(drain); err != nil {
		st.Close()
		srv.Close()
		return fmt.Errorf("drain: %w", err)
	}
	return nil
}
