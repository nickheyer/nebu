package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/config"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

// A whole daemon over a temp data directory with tls and auth off and the extra YAML, served until stop
type served struct {
	d    *Daemon
	base string
	done chan error
	stop context.CancelFunc
}

func serveDaemon(t *testing.T, extra string, before func(*Daemon)) *served {
	t.Helper()
	return serveConfigured(t, "auth:\n  disabled: true\n"+extra, before)
}

// A whole daemon over a temp data directory with tls off, the web UI off and the extra YAML, served until stop
func serveConfigured(t *testing.T, extra string, before func(*Daemon)) *served {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := fmt.Sprintf("listen: 127.0.0.1:0\ndata_dir: %s\ncache_dir: %s\nmin_free_bytes: 0\nweb:\n  disabled: true\n%s", filepath.Join(dir, "data"), filepath.Join(dir, "cache"), extra)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), new(slog.LevelVar), nil)
	if err != nil {
		t.Fatal(err)
	}
	if before != nil {
		before(d)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &served{d: d, base: "http://" + ln.Addr().String(), done: make(chan error, 1), stop: cancel}
	go func() { s.done <- d.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-s.done:
		case <-time.After(time.Minute):
			t.Error("the daemon did not stop")
		}
	})
	return s
}

// The status and Retry-After a GET answers
func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header.Get("Retry-After")
}

// The status of a JSON API call
func call(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// A readiness check that fails until opened
func heldCheck(open *atomic.Bool) func(*Daemon) {
	return func(d *Daemon) {
		d.Health.Register("held-by-test", func(context.Context) error {
			if !open.Load() {
				return errors.New("held by the test")
			}
			return nil
		})
	}
}

// On the shared listener the API opens after recovery while gateway paths answer 503 with Retry-After until readiness passes
func TestSharedGatewayPathsWaitForReadiness(t *testing.T) {
	var open atomic.Bool
	s := serveDaemon(t, "", heldCheck(&open))
	eventually(t, "the API open after recovery", func() bool { return call(t, s.base+"/nebu.v1.TaskService/ListTasks") == http.StatusOK })
	for range 5 {
		if code, retry := get(t, s.base+"/v1/models"); code != http.StatusServiceUnavailable || retry != "1" {
			t.Fatalf("a gateway path before readiness answered %d with Retry-After %q", code, retry)
		}
		if code := call(t, s.base+"/v1/chat/completions"); code != http.StatusServiceUnavailable {
			t.Fatalf("inference before readiness answered %d", code)
		}
		if code, _ := get(t, s.base+"/livez"); code != http.StatusOK {
			t.Fatalf("liveness before readiness %d", code)
		}
		if code, _ := get(t, s.base+"/readyz"); code != http.StatusServiceUnavailable {
			t.Fatalf("readiness with a failing check %d", code)
		}
		time.Sleep(60 * time.Millisecond)
	}
	if got := s.d.Gateway.Status().GetListeners(); len(got) != 0 {
		t.Fatalf("listeners reported before readiness %v", got)
	}
	open.Store(true)
	eventually(t, "gateway paths served once ready", func() bool {
		code, _ := get(t, s.base+"/v1/models")
		return code == http.StatusOK
	})
	if got := s.d.Gateway.Status().GetListeners(); len(got) != 1 || !got[0].GetShared() {
		t.Fatalf("listeners once ready %v", got)
	}
}

// A dedicated gateway listener binds nothing until readiness passes
func TestGatewayListenerBindsAfterReadiness(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	var open atomic.Bool
	s := serveDaemon(t, "gateway:\n  listen: "+addr+"\n", heldCheck(&open))
	eventually(t, "the API open after recovery", func() bool { return call(t, s.base+"/nebu.v1.TaskService/ListTasks") == http.StatusOK })
	for range 5 {
		if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			conn.Close()
			t.Fatal("the gateway listener accepted a connection before readiness")
		}
		if code, _ := get(t, s.base+"/v1/models"); code != http.StatusNotFound {
			t.Fatalf("the API listener answered a gateway path with %d while the gateway listens on its own address", code)
		}
		time.Sleep(60 * time.Millisecond)
	}
	open.Store(true)
	eventually(t, "the gateway listener serving once ready", func() bool {
		resp, err := http.Get("http://" + addr + "/v1/models")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
}

// The daemon reads an instance's scraped running and queued requests from the pool store, ignoring samples past the staleness
func TestScrapedLoadWiredToInstances(t *testing.T) {
	var running, queued atomic.Int32
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE vllm:num_requests_waiting gauge\nvllm:num_requests_waiting{model_name=\"m\"} %d\n", queued.Load())
		fmt.Fprintf(w, "# TYPE vllm:num_requests_running gauge\nvllm:num_requests_running{model_name=\"m\"} %d\n", running.Load())
		fmt.Fprintf(w, "# TYPE vllm:kv_cache_usage_perc gauge\nvllm:kv_cache_usage_perc{model_name=\"m\"} 0.1\n")
		fmt.Fprintf(w, "# TYPE vllm:cache_config_info gauge\nvllm:cache_config_info{block_size=\"16\",num_gpu_blocks=\"1024\"} 1\n")
	}))
	defer metrics.Close()
	s := serveDaemon(t, "gateway:\n  flow_control:\n    metrics_staleness_ms: 400\n", nil)
	eventually(t, "the daemon ready", func() bool { code, _ := get(t, s.base+"/readyz"); return code == http.StatusOK })
	if s.d.Instances.Load == nil {
		t.Fatal("the daemon wires no scrape reader into instances")
	}
	addr, err := datalayer.ParseAddress(strings.TrimPrefix(metrics.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	mem := pools.Membership{Model: pools.ModelKey{SourceID: "hf", Repo: "org/m", Group: "q4"}, Runtime: "vllm", Protocol: datalayer.AppProtocolHTTP, Concurrency: 4, Metrics: runtimes.VLLM{}.Metrics()}
	if err := s.d.Pools.Publish(context.Background(), pools.OwnerRef{Kind: pools.OwnerInstance, ID: "ext"}, mem, []pools.Published{{InstanceID: "ext", Address: addr, Ready: true}}); err != nil {
		t.Fatal(err)
	}
	running.Store(2)
	queued.Store(3)
	eventually(t, "the scraped load read through the instances manager", func() bool {
		r, q, scraped := s.d.Instances.Load("ext")
		return scraped && r == 2 && q == 3
	})
	running.Store(0)
	queued.Store(0)
	eventually(t, "an idle scrape", func() bool {
		r, q, scraped := s.d.Instances.Load("ext")
		return scraped && r == 0 && q == 0
	})
	metrics.Close()
	eventually(t, "the last sample going stale", func() bool {
		_, _, scraped := s.d.Instances.Load("ext")
		return !scraped
	})
}

// Writes an OpenAI stream chunk
func chunk(w http.ResponseWriter, content, finish string) {
	reason := "null"
	if finish != "" {
		reason = `"` + finish + `"`
	}
	fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"drained\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":%s}]}\n\n", content, reason)
	w.(http.Flusher).Flush()
}

// A shutdown lets a streamed request finish within the drain timeout and ends one that outlasts it at the timeout
func TestShutdownDrainsInFlightRequests(t *testing.T) {
	cases := []struct {
		name    string
		drainMs int
		release bool
	}{
		{"finishes within the drain timeout", 20000, true},
		{"ended at the drain timeout", 1500, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once atomic.Bool
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				chunk(w, "first", "")
				if once.CompareAndSwap(false, true) {
					close(started)
				}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				chunk(w, " last", "stop")
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer backend.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			s := serveDaemon(t, fmt.Sprintf("gateway:\n  drain_timeout_ms: %d\n", c.drainMs), nil)
			eventually(t, "the daemon ready", func() bool { code, _ := get(t, s.base+"/readyz"); return code == http.StatusOK })
			addr, err := datalayer.ParseAddress(strings.TrimPrefix(backend.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			owner := pools.OwnerRef{Kind: pools.OwnerInstance, ID: "backend"}
			mem := pools.Membership{Model: pools.ModelKey{SourceID: "hf", Repo: "org/drained", Group: "q4"}, Runtime: "vllm", Protocol: datalayer.AppProtocolHTTP, Concurrency: 4, Metrics: &v1.MetricNames{HealthPath: "/health"}}
			if err := s.d.Pools.Publish(context.Background(), owner, mem, []pools.Published{{InstanceID: "backend", Address: addr, Ready: true}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.d.Pools.NameRoute(context.Background(), owner, "drained", pools.RouteSpec{}); err != nil {
				t.Fatal(err)
			}
			eventually(t, "the route ready", func() bool {
				rt, ok := s.d.Pools.Route("drained")
				return ok && rt.GetStatus().GetState() == v1.RouteState_ROUTE_STATE_READY
			})
			type answer struct {
				body string
				err  error
			}
			answered := make(chan answer, 1)
			go func() {
				resp, err := http.Post(s.base+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"drained","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
				if err != nil {
					answered <- answer{err: err}
					return
				}
				defer resp.Body.Close()
				raw, err := io.ReadAll(resp.Body)
				answered <- answer{body: string(raw), err: err}
			}()
			select {
			case <-started:
			case <-time.After(20 * time.Second):
				t.Fatal("the request never reached the backend")
			}
			stopped := time.Now()
			s.stop()
			select {
			case err := <-s.done:
				s.done <- err
				t.Fatalf("serve returned %v while a request was in flight", err)
			case <-time.After(700 * time.Millisecond):
			}
			if c.release {
				close(release)
			}
			got := <-answered
			select {
			case err := <-s.done:
				if err != nil {
					t.Fatalf("serve returned %v", err)
				}
				s.done <- err
			case <-time.After(time.Minute):
				t.Fatal("the daemon did not stop")
			}
			took := time.Since(stopped)
			done := strings.Contains(got.body, "[DONE]") && strings.Contains(got.body, "last")
			switch {
			case c.release && (!done || got.err != nil):
				t.Fatalf("a request released within the drain timeout was cut: %v\n%s", got.err, got.body)
			case !c.release && done:
				t.Fatalf("a request outlasting the drain timeout finished anyway\n%s", got.body)
			case !c.release && (took < time.Duration(c.drainMs)*time.Millisecond || took > time.Duration(c.drainMs)*time.Millisecond+shutdownTimeout):
				t.Fatalf("the request was ended %s after shutdown began, the drain timeout is %dms", took, c.drainMs)
			}
		})
	}
}

// Every handoff a shipped runtime declares names a handoff plugin the gateway drives
func TestRuntimeHandoffsHaveAPlugin(t *testing.T) {
	reg, err := runtimes.New(runtimes.All())
	if err != nil {
		t.Fatal(err)
	}
	declared := 0
	for _, rt := range reg.List() {
		h := runtimeReader{reg}.Handoff(rt.ID())
		if h == "" {
			continue
		}
		declared++
		if !slices.Contains(requestcontrol.HandoffTypes(), h) {
			t.Fatalf("runtime %s declares handoff %q, the plugins are %v", rt.ID(), h, requestcontrol.HandoffTypes())
		}
	}
	if declared != len(requestcontrol.HandoffTypes()) {
		t.Fatalf("%d runtimes declare a handoff, %d plugins exist", declared, len(requestcontrol.HandoffTypes()))
	}
}
