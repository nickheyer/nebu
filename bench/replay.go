package bench

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/stub"
)

// Path every replayed request is sent to
const chatPath = "/v1/chat/completions"

// Failure messages a report keeps
const keptFailures = 10

// How a replay runs, zero values selecting the default named on each field
type Config struct {
	// Run name in the report, the scheduler configuration measured
	Name string
	// Trace time divisor, default one, two sending twice as fast
	Speed float64
	// Requests in flight at once, default 1024
	Concurrency int
	// Client the requests go through, default a transport sized to the concurrency
	Client *http.Client
	// Limit on each request, none when zero
	Timeout time.Duration
}

// Fills zero fields and refuses settings a replay cannot run
func (c Config) withDefaults() (Config, error) {
	if c.Speed == 0 {
		c.Speed = 1
	}
	if c.Concurrency == 0 {
		c.Concurrency = 1024
	}
	switch {
	case !(c.Speed > 0) || math.IsInf(c.Speed, 1):
		return c, fmt.Errorf("speed %v is not a finite positive factor", c.Speed)
	case c.Concurrency < 0:
		return c, fmt.Errorf("concurrency %d is negative", c.Concurrency)
	case c.Timeout < 0:
		return c, fmt.Errorf("timeout %v is negative", c.Timeout)
	}
	if c.Client == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.MaxIdleConnsPerHost = c.Concurrency
		c.Client = &http.Client{Transport: t}
	}
	return c, nil
}

// What one replayed request measured
type outcome struct {
	due, sent, end time.Time
	status         int
	failure        string
	endpoint       string
	stream         bool
	ttft, tpot     time.Duration
	hasTTFT        bool
	hasTPOT        bool
	promptTokens   int
	outputTokens   int
}

// Replays a JSONL trace file against a gateway and reports its latency and throughput
func Replay(ctx context.Context, gatewayURL, token, traceFile string, cfg Config) (*Report, error) {
	reqs, err := ReadFile(traceFile)
	if err != nil {
		return nil, err
	}
	rep, err := ReplayRequests(ctx, gatewayURL, token, reqs, cfg)
	if err != nil {
		return nil, err
	}
	rep.Trace = traceFile
	return rep, nil
}

// Sends each request at its trace offset divided by the speed through a bounded worker pool and measures every answer
func ReplayRequests(ctx context.Context, gatewayURL, token string, reqs []Request, cfg Config) (*Report, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	if len(reqs) == 0 {
		return nil, errors.New("the trace holds no requests")
	}
	bodies := make([][]byte, len(reqs))
	for i, r := range reqs {
		if err := r.validate(); err != nil {
			return nil, fmt.Errorf("request %d: %w", i, err)
		}
		if bodies[i], err = chatBody(i, r); err != nil {
			return nil, fmt.Errorf("request %d: %w", i, err)
		}
	}
	base, err := url.Parse(gatewayURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("gateway URL %q is not an http or https URL with a host", gatewayURL)
	}
	target := strings.TrimRight(gatewayURL, "/") + chatPath
	run, err := runID()
	if err != nil {
		return nil, err
	}
	order := make([]int, len(reqs))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(reqs[a].OffsetMS, reqs[b].OffsetMS) })
	outs := make([]outcome, len(reqs))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(cfg.Concurrency, len(reqs)) {
		wg.Go(func() {
			for i := range jobs {
				outs[i] = send(ctx, cfg, target, token, run+"-"+strconv.Itoa(i), reqs[i], bodies[i], outs[i].due)
			}
		})
	}
	start := time.Now()
	dispatched := dispatch(ctx, start, cfg.Speed, reqs, order, outs, jobs)
	close(jobs)
	wg.Wait()
	if dispatched < len(reqs) || ctx.Err() != nil {
		return nil, fmt.Errorf("replay stopped after %d of %d requests: %w", dispatched, len(reqs), context.Cause(ctx))
	}
	return buildReport(cfg, start, outs), nil
}

// Hands requests to workers at their scaled offsets, answering how many left before ctx ended
func dispatch(ctx context.Context, start time.Time, speed float64, reqs []Request, order []int, outs []outcome, jobs chan<- int) int {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for n, i := range order {
		due := start.Add(time.Duration(float64(reqs[i].Offset()) / speed))
		if wait := time.Until(due); wait > 0 {
			timer.Reset(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				return n
			}
		}
		outs[i].due = due
		select {
		case jobs <- i:
		case <-ctx.Done():
			return n
		}
	}
	return len(order)
}

// A random prefix that makes request ids of this run unique
func runID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "bench-" + hex.EncodeToString(b), nil
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatRequest struct {
	Model         string         `json:"model,omitempty"`
	Messages      []wireMessage  `json:"messages"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

// A chat completion message, its content a string or text and image parts
type wireMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type imageURL struct {
	URL string `json:"url"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

// The message as plain content, or as a text part followed by image parts when it carries images
func wireOf(m Message) wireMessage {
	if len(m.Images) == 0 {
		return wireMessage{Role: m.Role, Content: m.Content}
	}
	var parts []contentPart
	if m.Content != "" {
		parts = append(parts, contentPart{Type: "text", Text: m.Content})
	}
	for _, u := range m.Images {
		parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: u}})
	}
	return wireMessage{Role: m.Role, Content: parts}
}

// The chat completion body of a request, seeded words of the counted length standing in for a missing prompt
func chatBody(i int, r Request) ([]byte, error) {
	msgs := r.Messages
	switch {
	case len(msgs) > 0:
	case r.Prompt != "":
		msgs = []Message{{Role: "user", Content: r.Prompt}}
	default:
		rng := mrand.New(mrand.NewPCG(uint64(i), 0x6a09e667f3bcc908))
		msgs = []Message{{Role: "user", Content: words(rng, r.PromptTokens)}}
	}
	body := chatRequest{Model: r.Model, MaxTokens: r.MaxTokens, Stream: r.Stream}
	for _, m := range msgs {
		body.Messages = append(body.Messages, wireOf(m))
	}
	if r.Stream {
		body.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	return json.Marshal(body)
}

// Sends one request and measures its answer, streamed or whole
func send(ctx context.Context, cfg Config, target, token, id string, r Request, body []byte, due time.Time) outcome {
	out := outcome{due: due, stream: r.Stream}
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		out.sent = time.Now()
		out.end, out.failure = out.sent, err.Error()
		return out
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(requestcontrol.HeaderRequestID, id)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if r.Objective != "" {
		req.Header.Set(requestcontrol.HeaderObjective, r.Objective)
	}
	if r.FairnessID != "" {
		req.Header.Set(requestcontrol.HeaderFairnessID, r.FairnessID)
	}
	if r.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	out.sent = time.Now()
	resp, err := cfg.Client.Do(req)
	if err != nil {
		out.end, out.failure = time.Now(), err.Error()
		return out
	}
	defer resp.Body.Close()
	out.status = resp.StatusCode
	out.endpoint = resp.Header.Get(requestcontrol.HeaderServed)
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		out.end, out.failure = time.Now(), fmt.Sprintf("status %d %s", resp.StatusCode, errorMessage(msg))
		return out
	}
	if r.Stream {
		readStream(resp.Body, &out)
	} else {
		readWhole(resp.Body, &out)
	}
	return out
}

// The error message of a JSON error body, the trimmed body otherwise
func errorMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return strings.TrimSpace(string(body))
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type errorBody struct {
	Message string `json:"message"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string          `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			Reasoning        string          `json:"reasoning"`
			ToolCalls        json.RawMessage `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *usage     `json:"usage"`
	Error *errorBody `json:"error"`
}

// Whether a chunk carries answer text, reasoning or a tool call
func (c *streamChunk) content() bool {
	for _, ch := range c.Choices {
		d := ch.Delta
		calls := bytes.TrimSpace(d.ToolCalls)
		if d.Content != "" || d.ReasoningContent != "" || d.Reasoning != "" || len(calls) > 0 && string(calls) != "null" && string(calls) != "[]" {
			return true
		}
	}
	return false
}

// Reads server sent events, timing first and last content chunks, output tokens from usage or the chunk count
func readStream(body io.Reader, out *outcome) {
	br := bufio.NewReaderSize(body, 64<<10)
	var first, last time.Time
	chunks := 0
	var u *usage
	for {
		line, err := br.ReadBytes('\n')
		now := time.Now()
		if data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
			data = bytes.TrimSpace(data)
			if string(data) == "[DONE]" {
				out.end = now
				break
			}
			var c streamChunk
			if jerr := json.Unmarshal(data, &c); jerr != nil {
				out.end, out.failure = now, "malformed stream chunk: "+jerr.Error()
				return
			}
			if c.Error != nil {
				out.end, out.failure = now, "stream error: "+c.Error.Message
				return
			}
			if c.Usage != nil {
				u = c.Usage
			}
			if c.content() {
				if first.IsZero() {
					first = now
				}
				last = now
				chunks++
			}
		}
		if err != nil {
			out.end = now
			if errors.Is(err, io.EOF) {
				out.failure = "stream ended before [DONE]"
			} else {
				out.failure = "reading stream: " + err.Error()
			}
			return
		}
	}
	out.outputTokens = chunks
	if u != nil {
		out.outputTokens, out.promptTokens = u.CompletionTokens, u.PromptTokens
	}
	if !first.IsZero() {
		out.ttft, out.hasTTFT = first.Sub(out.sent), true
	}
	if chunks > 1 && out.outputTokens > 1 {
		out.tpot, out.hasTPOT = last.Sub(first)/time.Duration(out.outputTokens-1), true
	}
}

// Reads a whole completion, taking output tokens from usage or counting the answer's tokens
func readWhole(body io.Reader, out *outcome) {
	var c struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *usage `json:"usage"`
	}
	err := json.NewDecoder(body).Decode(&c)
	out.end = time.Now()
	if err != nil {
		out.failure = "malformed completion: " + err.Error()
		return
	}
	if c.Usage != nil {
		out.outputTokens, out.promptTokens = c.Usage.CompletionTokens, c.Usage.PromptTokens
		return
	}
	for _, ch := range c.Choices {
		out.outputTokens += len(stub.Tokenize(ch.Message.Content))
	}
}

// Folds outcomes into counts, percentiles and rates over the wall time from start to the last answer
func buildReport(cfg Config, start time.Time, outs []outcome) *Report {
	rep := &Report{Name: cfg.Name, Speed: cfg.Speed, Concurrency: cfg.Concurrency, Requests: len(outs), Statuses: map[int]int{}, Endpoints: map[string]int{}}
	var ttft, tpot, e2e, lag []float64
	end := start
	seen := map[string]bool{}
	for _, o := range outs {
		if o.end.After(end) {
			end = o.end
		}
		lag = append(lag, ms(o.sent.Sub(o.due)))
		if o.endpoint != "" {
			rep.Endpoints[o.endpoint]++
		}
		if o.status != 0 && o.status != http.StatusOK {
			rep.Statuses[o.status]++
		}
		if o.failure != "" {
			switch o.status {
			case http.StatusTooManyRequests:
				rep.Shed++
			case http.StatusServiceUnavailable:
				rep.Unavailable++
			default:
				rep.Errors++
			}
			if !seen[o.failure] && len(rep.Failures) < keptFailures {
				seen[o.failure] = true
				rep.Failures = append(rep.Failures, o.failure)
			}
			continue
		}
		rep.Succeeded++
		rep.PromptTokens += int64(o.promptTokens)
		rep.OutputTokens += int64(o.outputTokens)
		e2e = append(e2e, ms(o.end.Sub(o.sent)))
		if o.stream {
			rep.Streamed++
		}
		if o.hasTTFT {
			ttft = append(ttft, ms(o.ttft))
		}
		if o.hasTPOT {
			tpot = append(tpot, ms(o.tpot))
		}
	}
	wall := end.Sub(start)
	rep.DurationMS = ms(wall)
	if wall > 0 {
		rep.RequestsPerSecond = float64(rep.Succeeded) / wall.Seconds()
		rep.OutputTokensPerSecond = float64(rep.OutputTokens) / wall.Seconds()
	}
	rep.TTFT, rep.TPOT, rep.E2E, rep.Lag = summarize(ttft), summarize(tpot), summarize(e2e), summarize(lag)
	return rep
}

// A duration in fractional milliseconds
func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
