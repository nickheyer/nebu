// Package stub emulates an LLM inference server without an accelerator for scheduler tests and trace replay
package stub

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// Metric naming scheme served on /metrics
type Names string

const (
	NamesVLLM     Names = "vllm"
	NamesSGLang   Names = "sglang"
	NamesLlamaCpp Names = "llamacpp"
)

// How emulated durations map onto wall time
type Mode string

const (
	// Sleeps every emulated duration in full
	ModeTime Mode = "time"
	// Sleeps every emulated duration divided by the speedup
	ModeRatio Mode = "ratio"
)

// LoRA resident from startup
type Adapter struct {
	Name string
	Path string
}

// Stub settings, zero values selecting the default named on each field
type Config struct {
	// Value of the X-Stub-Id header, default stub
	ID string
	// Base model name, default stub-model
	Model string
	// Adapters resident from startup, at most MaxLoras
	Adapters []Adapter
	// Metric names served, default vllm
	Names Names
	// Running sequence cap, default 256
	MaxNumSeqs int
	// KV cache size in blocks, default 4096
	NumGPUBlocks int
	// Tokens per KV block, default 16
	BlockSize int
	// Adapter slots, default 4
	MaxLoras int
	// Emulated time an adapter takes to load
	LoraLoadDelay time.Duration
	// Wall time after New during which health and the model endpoints answer 503
	LoadTime time.Duration
	// Emulated fixed cost of every prefill
	BaseLatency time.Duration
	// Emulated prefill cost per uncached prompt token
	PrefillPerToken time.Duration
	// Emulated cost per decoded token
	DecodePerToken time.Duration
	// Step slowdown added per other running sequence as a fraction of the step cost
	Contention float64
	// Time mapping, default time
	Mode Mode
	// Wall time divisor in ratio mode
	Speedup float64
	// Output words when a request sets no max_tokens, default 16
	DefaultMaxTokens int
	// Per request records kept for Stats with the oldest dropped first, zero keeping all
	Records int
	// A real tokenizer in place of the hashed one, encoding text prompts and spelling answer token ids
	Tokenizer Tokenizer
}

// Encodes text to ids and decodes ids back, for a stub that speaks a model's own token ids
type Tokenizer interface {
	Encode(text string, addSpecialTokens bool) ([]uint32, error)
	Decode(ids []uint32) (string, error)
}

// Fills zero fields and rejects settings the engine cannot run
func (c Config) withDefaults() (Config, error) {
	if c.ID == "" {
		c.ID = "stub"
	}
	if c.Model == "" {
		c.Model = "stub-model"
	}
	if c.Names == "" {
		c.Names = NamesVLLM
	}
	if c.MaxNumSeqs == 0 {
		c.MaxNumSeqs = 256
	}
	if c.NumGPUBlocks == 0 {
		c.NumGPUBlocks = 4096
	}
	if c.BlockSize == 0 {
		c.BlockSize = 16
	}
	if c.MaxLoras == 0 {
		c.MaxLoras = 4
	}
	if c.Mode == "" {
		c.Mode = ModeTime
	}
	if c.DefaultMaxTokens == 0 {
		c.DefaultMaxTokens = 16
	}
	switch c.Names {
	case NamesVLLM, NamesSGLang, NamesLlamaCpp:
	default:
		return c, fmt.Errorf("stub: unknown metric names %q", c.Names)
	}
	switch c.Mode {
	case ModeTime:
	case ModeRatio:
		if !(c.Speedup > 0) || math.IsInf(c.Speedup, 0) {
			return c, errors.New("stub: ratio mode needs a finite positive speedup")
		}
	default:
		return c, fmt.Errorf("stub: unknown mode %q", c.Mode)
	}
	if c.MaxNumSeqs < 0 || c.NumGPUBlocks < 0 || c.BlockSize < 0 || c.MaxLoras < 0 || c.DefaultMaxTokens < 0 || c.Records < 0 {
		return c, errors.New("stub: sizes and counts must not be negative")
	}
	if c.LoraLoadDelay < 0 || c.LoadTime < 0 || c.BaseLatency < 0 || c.PrefillPerToken < 0 || c.DecodePerToken < 0 {
		return c, errors.New("stub: durations must not be negative")
	}
	if !(c.Contention >= 0) || math.IsInf(c.Contention, 0) {
		return c, errors.New("stub: contention must be a finite non-negative number")
	}
	if len(c.Adapters) > c.MaxLoras {
		return c, fmt.Errorf("stub: %d adapters exceed %d adapter slots", len(c.Adapters), c.MaxLoras)
	}
	seen := map[string]bool{c.Model: true}
	for _, a := range c.Adapters {
		if a.Name == "" || a.Path == "" {
			return c, errors.New("stub: adapters need a name and a path")
		}
		if seen[a.Name] {
			return c, fmt.Errorf("stub: adapter %q repeats a served name", a.Name)
		}
		seen[a.Name] = true
	}
	return c, nil
}

// A running emulated model server, usable as an http.Handler
type Stub struct {
	cfg     Config
	eng     *engine
	mux     *http.ServeMux
	created int64
	counter atomic.Uint64
}

// Validates the settings and starts the scheduler loop
func New(cfg Config) (*Stub, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s := &Stub{cfg: cfg, eng: newEngine(cfg, now), mux: http.NewServeMux(), created: now.Unix()}
	s.mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	s.mux.HandleFunc("POST /v1/completions", s.handleCompletions)
	s.mux.HandleFunc("GET /v1/models", s.handleModels)
	s.mux.HandleFunc("POST /v1/load_lora_adapter", s.handleLoad)
	s.mux.HandleFunc("POST /v1/unload_lora_adapter", s.handleUnload)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
	s.mux.HandleFunc("GET /stub/control", s.handleFaults)
	s.mux.HandleFunc("POST /stub/control", s.handleControl)
	s.mux.HandleFunc("GET /stub/stats", s.handleStats)
	switch cfg.Names {
	case NamesSGLang:
		s.mux.HandleFunc("GET /server_info", s.handleServerInfo)
		s.mux.HandleFunc("GET /get_server_info", s.handleServerInfo)
	case NamesLlamaCpp:
		s.mux.HandleFunc("GET /slots", s.handleSlots)
		s.mux.HandleFunc("GET /lora-adapters", s.handleLoraAdapters)
	}
	go s.eng.loop()
	return s, nil
}

// Serves the API with identity headers and the echoed request id on every response
func (s *Stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Served-By", servedBy(r))
	h.Set("X-Stub-Id", s.cfg.ID)
	if id := r.Header.Get("X-Request-Id"); id != "" {
		h.Set("X-Request-Id", id)
	}
	s.mux.ServeHTTP(w, r)
}

// Fails pending requests with 503, stops the scheduler loop and closes every Served channel
func (s *Stub) Close() { s.eng.close() }

// Counters and kept per request records at this moment
func (s *Stub) Stats() Stats { return s.eng.stats() }

// KV cache tokens of the whole pool, blocks times tokens per block
func (s *Stub) KVCapacity() int { return s.cfg.NumGPUBlocks * s.cfg.BlockSize }

// A new channel receiving a Record for each request completed after this call, closed by Close
func (s *Stub) Served() <-chan Record { return s.eng.subscribe() }

// Holds every queued and arriving request out of the running set and fails health checks
func (s *Stub) Pause() { s.eng.setPaused(true) }

// Lets queued requests run again and passes health checks
func (s *Stub) Resume() { s.eng.setPaused(false) }

// Validates and applies a fault update, returning the faults in force afterward
func (s *Stub) Control(c Control) (FaultState, error) { return s.eng.control(c) }

// Id taken from X-Request-Id or numbered by the stub
func (s *Stub) requestID(r *http.Request) string {
	if id := r.Header.Get("X-Request-Id"); id != "" {
		return id
	}
	return s.cfg.ID + "-" + strconv.FormatUint(s.counter.Add(1), 10)
}

// The local host and port the request arrived on, or the request host when no listener carried it
func servedBy(r *http.Request) string {
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		return a.String()
	}
	return r.Host
}
