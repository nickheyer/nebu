// Package scrape samples each endpoint's Prometheus metrics on a fixed interval and maps them to server metrics
package scrape

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/datalayer/internal/poll"
)

const (
	Type            = "metrics"
	DefaultInterval = 50 * time.Millisecond
	// Scrapes in a row that must fail before the failure is set on the endpoint
	FailureThreshold = 3
	// Largest metrics or slot document read from one endpoint
	maxBody = 32 << 20
	accept  = "text/plain;version=0.0.4;q=1,*/*;q=0.1"
)

var (
	ErrUnresolved = errors.New("endpoint resolves to no metrics address")
	ErrTooLarge   = errors.New("metrics document exceeds the size cap")
)

// Resolves an endpoint to the metric names its pool declares and the address serving them
type Resolver func(datalayer.EndpointID) (datalayer.MetricCapabilities, datalayer.Address, bool)

// Receives the pool, duration and outcome of every scrape
type Observer interface {
	OnScrape(pool datalayer.PoolID, endpoint datalayer.EndpointID, duration time.Duration, err error)
}

type Options struct {
	// Time between scrapes of one endpoint and the timeout of each, DefaultInterval when zero
	Interval time.Duration
	// Told about every scrape when set
	Observer Observer
	// Opens the scrape connections, the plain dialer when nil
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// One scrape of an endpoint with the names its pool declares
type Sample struct {
	Families     map[string]*dto.MetricFamily
	Slots        []Slot
	Adapters     []Adapter
	MaxLoRA      *int
	Capabilities datalayer.MetricCapabilities
	ScrapedAt    time.Time
}

// One llama.cpp server slot, its context and the tokens its cache holds, generated tokens included
type Slot struct {
	IsProcessing  bool `json:"is_processing"`
	NCtx          int  `json:"n_ctx"`
	NPromptTokens int  `json:"n_prompt_tokens"`
}

// One adapter a runtime lists, applied when requests may name it
type Adapter struct {
	Name    string
	Applied bool
}

// Scrapes every tracked endpoint from its own goroutine
type Source struct {
	store    datalayer.Store
	resolve  Resolver
	interval time.Duration
	observer Observer
	client   *http.Client
	cols     poll.Collectors
	group    *poll.Group
	mu       sync.Mutex
	fails    map[datalayer.EndpointID]*failures
}

// Failed scrapes in a row of one endpoint, the first one's time and the latest error
type failures struct {
	count int
	since time.Time
	err   string
}

// Builds a source that stores collector output in store
func New(store datalayer.Store, resolve Resolver, opts Options) *Source {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	s := &Source{
		store:    store,
		resolve:  resolve,
		interval: opts.Interval,
		observer: opts.Observer,
		client:   newClient(opts.Dial),
		fails:    map[datalayer.EndpointID]*failures{},
	}
	s.group = poll.NewGroup(opts.Interval, s.scrape, s.untrack)
	return s
}

// Clears what the collectors and the failure count stored for an endpoint no longer scraped
func (s *Source) untrack(id datalayer.EndpointID) {
	s.cols.Clear(s.store, id)
	s.mu.Lock()
	delete(s.fails, id)
	s.mu.Unlock()
	s.store.SetAttribute(id, datalayer.CollectorScrapeFailure, nil)
}

// Counts failures in a row, setting the failure on the endpoint once it repeats or changes and clearing it on success
func (s *Source) record(id datalayer.EndpointID, err error, at time.Time) {
	s.mu.Lock()
	f := s.fails[id]
	if err == nil {
		delete(s.fails, id)
		s.mu.Unlock()
		if f != nil && f.count >= FailureThreshold {
			s.store.SetAttribute(id, datalayer.CollectorScrapeFailure, nil)
		}
		return
	}
	if f == nil {
		f = &failures{since: at}
		s.fails[id] = f
	}
	changed := f.err != err.Error()
	f.count, f.err = f.count+1, err.Error()
	set := f.count == FailureThreshold || (f.count > FailureThreshold && changed)
	failure := &datalayer.ScrapeFailure{Err: f.err, Since: f.since}
	s.mu.Unlock()
	if set {
		s.store.SetAttribute(id, datalayer.CollectorScrapeFailure, failure)
	}
}

func (s *Source) Type() string                                     { return Type }
func (s *Source) Start(ctx context.Context) error                  { return s.group.Start(ctx) }
func (s *Source) Stop() error                                      { return s.group.Stop() }
func (s *Source) Subscribe(c datalayer.Collector) error            { return s.cols.Subscribe(c) }
func (s *Source) UpdateEndpoints(ids []datalayer.EndpointID) error { return s.group.Update(ids) }

// Keeps pools that declare complete metric names and a metrics path
func (s *Source) Tracks(p datalayer.Pool) bool { return p.Metrics.Complete() && p.Metrics.Path != "" }

// Scrapes one endpoint, hands the sample to every collector and reports the outcome
func (s *Source) scrape(ctx context.Context, id datalayer.EndpointID) {
	start := time.Now()
	caps, addr, ok := s.resolve(id)
	var err error
	if !ok {
		err = fmt.Errorf("%w: %s", ErrUnresolved, id)
	} else {
		var sample *Sample
		sample, err = s.fetch(ctx, caps, addr)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = s.cols.Deliver(s.store, id, sample)
		}
	}
	s.record(id, err, start)
	if s.observer != nil {
		s.observer.OnScrape(s.poolOf(id), id, time.Since(start), err)
	}
}

// The pool an endpoint belongs to, empty for one the store no longer holds
func (s *Source) poolOf(id datalayer.EndpointID) datalayer.PoolID {
	if ep, ok := s.store.Endpoint(id); ok {
		return ep.Pool()
	}
	return ""
}

// Reads the metrics document and every JSON listing the pool declares beside it
func (s *Source) fetch(ctx context.Context, caps datalayer.MetricCapabilities, addr datalayer.Address) (*Sample, error) {
	ctx, cancel := context.WithTimeout(ctx, s.interval)
	defer cancel()
	base := "http://" + addr.String()
	sample := &Sample{Capabilities: caps}
	err := get(ctx, s.client, base+caps.Path, func(resp *http.Response) error {
		fams, err := decode(limit(resp.Body), expfmt.ResponseFormat(resp.Header))
		sample.Families = fams
		return err
	})
	if err != nil {
		return nil, err
	}
	sample.ScrapedAt = time.Now()
	if caps.KVCacheUtilization == "" && caps.SlotsPath != "" {
		if err := getJSON(ctx, s.client, base+caps.SlotsPath, &sample.Slots); err != nil {
			return nil, err
		}
	}
	if caps.AdaptersPath != "" {
		if sample.Adapters, err = s.adapters(ctx, base, caps); err != nil {
			return nil, err
		}
	}
	if caps.MaxLoRAPath != "" {
		var doc map[string]json.RawMessage
		if err := getJSON(ctx, s.client, base+caps.MaxLoRAPath, &doc); err != nil {
			return nil, err
		}
		if raw, ok := doc[caps.MaxLoRAField]; ok {
			var n int
			if err := json.Unmarshal(raw, &n); err != nil {
				return nil, fmt.Errorf("GET %s: %s: %w", base+caps.MaxLoRAPath, caps.MaxLoRAField, err)
			}
			sample.MaxLoRA = &n
		}
	}
	return sample, nil
}

// Reads the adapter listing in the format the pool declares
func (s *Source) adapters(ctx context.Context, base string, caps datalayer.MetricCapabilities) ([]Adapter, error) {
	url := base + caps.AdaptersPath
	switch caps.AdaptersFormat {
	case datalayer.AdaptersLlamaCpp:
		var list []struct {
			Path  string  `json:"path"`
			Scale float64 `json:"scale"`
		}
		if err := getJSON(ctx, s.client, url, &list); err != nil {
			return nil, err
		}
		out := make([]Adapter, 0, len(list))
		for _, a := range list {
			out = append(out, Adapter{Name: adapterName(a.Path), Applied: a.Scale > 0})
		}
		return out, nil
	case datalayer.AdaptersOpenAI:
		var listing struct {
			Data []struct {
				ID     string `json:"id"`
				Parent string `json:"parent"`
			} `json:"data"`
		}
		if err := getJSON(ctx, s.client, url, &listing); err != nil {
			return nil, err
		}
		var out []Adapter
		for _, m := range listing.Data {
			if m.Parent != "" {
				out = append(out, Adapter{Name: m.ID, Applied: true})
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrAdapterFormat, caps.AdaptersFormat)
}

// The adapter file's base without directory or extension, as the catalog names it
func adapterName(file string) string {
	base := path.Base(strings.ReplaceAll(file, `\`, "/"))
	return strings.TrimSuffix(base, path.Ext(base))
}

// Issues an HTTP request and hands a 2xx response to the reader
func get(ctx context.Context, client *http.Client, url string, read func(*http.Response) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", accept)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		return fmt.Errorf("GET %s answered %s", url, resp.Status)
	}
	if err := read(resp); err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	return nil
}

// Decodes a 2xx JSON response into out
func getJSON(ctx context.Context, client *http.Client, url string, out any) error {
	return get(ctx, client, url, func(resp *http.Response) error {
		return json.NewDecoder(limit(resp.Body)).Decode(out)
	})
}

// Parses every family of an exposition document in the format the response declares
func decode(r io.Reader, format expfmt.Format) (map[string]*dto.MetricFamily, error) {
	dec := expfmt.NewDecoder(r, format)
	fams := map[string]*dto.MetricFamily{}
	for {
		mf := &dto.MetricFamily{}
		if err := dec.Decode(mf); err != nil {
			if errors.Is(err, io.EOF) {
				return fams, nil
			}
			return nil, err
		}
		fams[mf.GetName()] = mf
	}
}

// Wraps a body so reading past the size cap fails instead of truncating
func limit(r io.Reader) io.Reader { return &capped{r: r, left: maxBody} }

type capped struct {
	r    io.Reader
	left int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var one [1]byte
		n, err := c.r.Read(one[:])
		if n > 0 {
			return 0, ErrTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// A client with its own keep-alive pool that never routes endpoint traffic through a proxy
func newClient(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	if dial != nil {
		t.DialContext = dial
	}
	t.Proxy = nil
	return &http.Client{Transport: t}
}

// Resolves endpoints to their pool's metric names and address, applying the pool's metrics port
func StoreResolver(store datalayer.Store) Resolver {
	return func(id datalayer.EndpointID) (datalayer.MetricCapabilities, datalayer.Address, bool) {
		ep, ok := store.Endpoint(id)
		if !ok {
			return datalayer.MetricCapabilities{}, datalayer.Address{}, false
		}
		p, ok := store.Pool(ep.Pool())
		if !ok || !p.Metrics.Complete() || p.Metrics.Path == "" {
			return datalayer.MetricCapabilities{}, datalayer.Address{}, false
		}
		addr := ep.Address()
		if p.Metrics.MetricsPort != 0 {
			addr.Port = p.Metrics.MetricsPort
		}
		return p.Metrics, addr, true
	}
}
