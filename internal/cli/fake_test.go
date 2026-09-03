package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
)

const (
	fakeEnv        = "NEBU_FAKE_RUNTIME"
	fakeFail       = "NEBU_FAKE_FAIL"
	fakeIgnoreTerm = "NEBU_FAKE_IGNORE_TERM"
	serveEnv       = "NEBU_TEST_SERVE"
)

// Turns the test binary into a fake runtime or a real daemon when asked
func TestMain(m *testing.M) {
	if os.Getenv(serveEnv) == "1" {
		os.Unsetenv(serveEnv)
		os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
	}
	if os.Getenv(fakeEnv) == "1" {
		os.Exit(fakeRuntime(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeRuntime(args []string) int {
	flags := map[string]string{}
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--") && i+1 < len(args) {
			flags[args[i]] = args[i+1]
			i++
		}
	}
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("fake version 1.2.3")
		return 0
	}
	fmt.Println("load_tensors: CUDA0 model buffer size = 10.00 MiB")
	fmt.Println("llama_context: CUDA0 compute buffer size = 5.00 MiB")
	fmt.Printf("model=%s ctx=%s layers=%s\n", flags["--model"], flags["--ctx-size"], flags["--n-gpu-layers"])
	if os.Getenv(fakeFail) == "1" {
		fmt.Fprintln(os.Stderr, "GGML_ASSERT: boom")
		return 1
	}
	addr := net.JoinHostPort(flags["--host"], flags["--port"])
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(map[string]any{"model": req["model"], "choices": []map[string]any{{"message": map[string]any{"content": "hello from fake"}}}})
	})
	srv := &http.Server{Addr: addr, Handler: mux}
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if os.Getenv(fakeIgnoreTerm) == "1" {
		signal.Ignore(syscall.SIGTERM)
		signals = signals[:1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), signals...)
	defer stop()
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	fmt.Println("listening on", addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
