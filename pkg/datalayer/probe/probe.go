// Package probe checks each endpoint's health path on an interval and tracks healthy and unhealthy transitions
package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/datalayer/internal/poll"
)

const (
	Type             = "health"
	DefaultInterval  = time.Second
	DefaultThreshold = 3
	// Most of a health response body read before the connection is reused
	maxBody = 64 << 10
)

var (
	ErrUnresolved = errors.New("endpoint resolves to no health url")
	ErrSampleType = errors.New("sample is not a health probe result")
)

// Resolves an endpoint to the URL its health is probed at
type Resolver func(datalayer.EndpointID) (url string, ok bool)

// Receives the duration and failure of every probe, a nil error for a passing probe
type Observer interface {
	OnProbe(endpoint datalayer.EndpointID, duration time.Duration, err error)
}

type Options struct {
	// Time between probes of one endpoint and the timeout of each, DefaultInterval when zero
	Interval time.Duration
	// Told about every probe when set
	Observer Observer
	// Opens the probe connections, the plain dialer when nil
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// The outcome of one probe
type Result struct {
	OK        bool
	Message   string
	CheckedAt time.Time
}

// Checks every tracked endpoint's health from its own goroutine
type Source struct {
	store    datalayer.Store
	resolve  Resolver
	observer Observer
	client   *http.Client
	cols     poll.Collectors
	group    *poll.Group
}

// Builds a source that stores collector output in store
func New(store datalayer.Store, resolve Resolver, opts Options) *Source {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	if opts.Dial != nil {
		t.DialContext = opts.Dial
	}
	s := &Source{
		store:    store,
		resolve:  resolve,
		observer: opts.Observer,
		client:   &http.Client{Transport: t, Timeout: opts.Interval},
	}
	s.group = poll.NewGroup(opts.Interval, s.probe, func(id datalayer.EndpointID) { s.cols.Clear(store, id) })
	return s
}

func (s *Source) Type() string                                     { return Type }
func (s *Source) Start(ctx context.Context) error                  { return s.group.Start(ctx) }
func (s *Source) Stop() error                                      { return s.group.Stop() }
func (s *Source) Subscribe(c datalayer.Collector) error            { return s.cols.Subscribe(c) }
func (s *Source) UpdateEndpoints(ids []datalayer.EndpointID) error { return s.group.Update(ids) }

// Checks one endpoint of any pool, one whose pool names no health path failing, and hands the result to every collector
func (s *Source) probe(ctx context.Context, id datalayer.EndpointID) {
	start := time.Now()
	var err error
	if url, ok := s.resolve(id); !ok {
		err = fmt.Errorf("%w: %s", ErrUnresolved, id)
	} else if err = s.check(ctx, url); ctx.Err() != nil {
		return
	}
	r := &Result{OK: err == nil, CheckedAt: time.Now()}
	if err != nil {
		r.Message = err.Error()
	}
	if derr := s.cols.Deliver(s.store, id, r); derr != nil {
		err = errors.Join(err, derr)
	}
	if s.observer != nil {
		s.observer.OnProbe(id, time.Since(start), err)
	}
}

// A GET answered with a 2xx status passes
func (s *Source) check(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("GET %s answered %s", url, resp.Status)
	}
	return nil
}

// Turns probe results into health state, unhealthy after a run of failures and healthy after one success
type Collector struct {
	threshold int
}

// Builds a collector marking an endpoint unhealthy after threshold consecutive failures, DefaultThreshold when not positive
func NewCollector(threshold int) *Collector {
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	return &Collector{threshold: threshold}
}

func (c *Collector) Name() string { return datalayer.CollectorHealth }

// Extends the endpoint's failure run on a failed probe and resets it on a passing one
func (c *Collector) Extract(ep datalayer.Endpoint, sample any) (any, error) {
	r, ok := sample.(*Result)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrSampleType, sample)
	}
	if r.OK {
		return &datalayer.HealthState{Healthy: true, CheckedAt: r.CheckedAt}, nil
	}
	failures := 1
	if prev, ok := datalayer.Attribute[*datalayer.HealthState](ep, datalayer.CollectorHealth); ok {
		failures = prev.Failures + 1
	}
	return &datalayer.HealthState{
		Healthy:   failures < c.threshold,
		CheckedAt: r.CheckedAt,
		Failures:  failures,
		Message:   r.Message,
	}, nil
}

// Resolves endpoints to the health path of their pool on the endpoint's own address
func StoreResolver(store datalayer.Store) Resolver {
	return func(id datalayer.EndpointID) (string, bool) {
		ep, ok := store.Endpoint(id)
		if !ok {
			return "", false
		}
		p, ok := store.Pool(ep.Pool())
		if !ok || p.Metrics.HealthPath == "" {
			return "", false
		}
		return "http://" + ep.Address().String() + p.Metrics.HealthPath, true
	}
}
