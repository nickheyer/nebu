// Package catalog lists the models and adapters each endpoint serves from its OpenAI model listing
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/datalayer/internal/poll"
)

const (
	Type            = "catalog"
	DefaultInterval = 30 * time.Second
	// Largest model or adapter listing read from one endpoint
	maxBody = 8 << 20
)

var (
	ErrUnresolved = errors.New("endpoint resolves to no model listing")
	ErrSampleType = errors.New("sample is not a catalog fetch")
)

// Resolves an endpoint to the base URL of its OpenAI API and the model its pool serves
type Resolver func(datalayer.EndpointID) (baseURL, model string, ok bool)

// Receives the duration and outcome of every catalog fetch
type Observer interface {
	OnFetch(endpoint datalayer.EndpointID, duration time.Duration, err error)
}

type Options struct {
	// Time between fetches from one endpoint and the timeout of each, DefaultInterval when zero
	Interval time.Duration
	// Told about every fetch when set
	Observer Observer
	// Opens the catalog connections, the plain dialer when nil
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// One fetch of an endpoint's model listing and, when it serves one, its LoRA adapter listing
type Sample struct {
	PoolModel string
	Models    []Model
	LoRA      []LoRA
	FetchedAt time.Time
}

// One entry of a /v1/models listing
type Model struct {
	ID     string `json:"id"`
	Parent string `json:"parent"`
}

// One entry of the adapter listing a llama.cpp server serves
type LoRA struct {
	Path string `json:"path"`
}

// The adapter file's base without directory or extension
func (l LoRA) Name() string {
	base := path.Base(strings.ReplaceAll(l.Path, `\`, "/"))
	return strings.TrimSuffix(base, path.Ext(base))
}

// Fetches every tracked endpoint's listings from its own goroutine
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
	s.group = poll.NewGroup(opts.Interval, s.fetch, func(id datalayer.EndpointID) { s.cols.Clear(store, id) })
	return s
}

func (s *Source) Type() string                                     { return Type }
func (s *Source) Start(ctx context.Context) error                  { return s.group.Start(ctx) }
func (s *Source) Stop() error                                      { return s.group.Stop() }
func (s *Source) Subscribe(c datalayer.Collector) error            { return s.cols.Subscribe(c) }
func (s *Source) UpdateEndpoints(ids []datalayer.EndpointID) error { return s.group.Update(ids) }

// Keeps pools whose endpoints speak the OpenAI HTTP API
func (s *Source) Tracks(p datalayer.Pool) bool { return p.AppProtocol != datalayer.AppProtocolGRPC }

// Fetches one endpoint's listings, hands them to every collector and reports the outcome
func (s *Source) fetch(ctx context.Context, id datalayer.EndpointID) {
	start := time.Now()
	var err error
	if base, model, ok := s.resolve(id); !ok {
		err = fmt.Errorf("%w: %s", ErrUnresolved, id)
	} else {
		var sample *Sample
		sample, err = s.listings(ctx, strings.TrimSuffix(base, "/"), model)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = s.cols.Deliver(s.store, id, sample)
		}
	}
	if s.observer != nil {
		s.observer.OnFetch(id, time.Since(start), err)
	}
}

// Reads /v1/models and /lora-adapters, the second absent when the server answers 404 or 405
func (s *Source) listings(ctx context.Context, base, model string) (*Sample, error) {
	var models struct {
		Data []Model `json:"data"`
	}
	if err := s.getJSON(ctx, base+"/v1/models", false, &models); err != nil {
		return nil, err
	}
	sample := &Sample{PoolModel: model, Models: models.Data}
	if err := s.getJSON(ctx, base+"/lora-adapters", true, &sample.LoRA); err != nil {
		return nil, err
	}
	sample.FetchedAt = time.Now()
	return sample, nil
}

// Decodes a JSON GET into out, an optional route answering 404 or 405 leaving out untouched
func (s *Source) getJSON(ctx context.Context, url string, optional bool, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, maxBody+1)
	absent := resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, body)
		if optional && absent {
			return nil
		}
		return fmt.Errorf("GET %s answered %s", url, resp.Status)
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	if len(raw) > maxBody {
		return fmt.Errorf("GET %s: listing exceeds %d bytes", url, maxBody)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	return nil
}

// Splits listings into base models and adapters
type Collector struct{}

func (Collector) Name() string { return datalayer.CollectorCatalog }

// An entry other than the pool model with a parent, or named in the adapter listing, is an adapter
func (Collector) Extract(_ datalayer.Endpoint, sample any) (any, error) {
	s, ok := sample.(*Sample)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrSampleType, sample)
	}
	listed := make(map[string]struct{}, len(s.LoRA))
	var adapters []string
	for _, l := range s.LoRA {
		name := l.Name()
		if name == "" {
			continue
		}
		listed[name] = struct{}{}
		adapters = append(adapters, name)
	}
	var models []string
	for _, m := range s.Models {
		if m.ID == "" {
			continue
		}
		_, inListing := listed[m.ID]
		if m.ID != s.PoolModel && (m.Parent != "" || inListing) {
			adapters = append(adapters, m.ID)
			continue
		}
		models = append(models, m.ID)
	}
	slices.Sort(models)
	slices.Sort(adapters)
	return &datalayer.Catalog{
		Models:    slices.Compact(models),
		Adapters:  slices.Compact(adapters),
		UpdatedAt: s.FetchedAt,
	}, nil
}

// Resolves endpoints to their own address and the model their pool serves
func StoreResolver(store datalayer.Store) Resolver {
	return func(id datalayer.EndpointID) (string, string, bool) {
		ep, ok := store.Endpoint(id)
		if !ok {
			return "", "", false
		}
		p, ok := store.Pool(ep.Pool())
		if !ok || p.AppProtocol == datalayer.AppProtocolGRPC {
			return "", "", false
		}
		return "http://" + ep.Address().String(), p.Model, true
	}
}
