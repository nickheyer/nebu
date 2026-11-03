package bench

import (
	"context"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/datalayer"
	scrapesrc "github.com/nickheyer/nebu/pkg/datalayer/scrape"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/stub"
)

// Receives the outcome of the first scrape only
type firstScrape chan error

func (f firstScrape) OnScrape(_ datalayer.PoolID, _ datalayer.EndpointID, _ time.Duration, err error) {
	select {
	case f <- err:
	default:
	}
}

// Server metrics of one gateway scrape of a stub address and its labels through the real scrape table of the mode
func scrapeOnce(t *testing.T, mode stub.Names, address string, labels map[string]string) (*datalayer.ServerMetrics, error) {
	t.Helper()
	caps, ok := scrapesrc.Capabilities(string(mode))
	if !ok {
		t.Fatalf("scrape table lacks %s", mode)
	}
	addr, err := datalayer.ParseAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	store := datalayer.NewStore()
	store.PutPool(datalayer.Pool{ID: "p", Runtime: string(mode), Metrics: caps})
	store.UpsertEndpoint(datalayer.EndpointSpec{ID: "e", Pool: "p", Address: addr, Labels: labels, Ready: true})
	first := firstScrape(make(chan error, 1))
	src := scrapesrc.New(store, scrapesrc.StoreResolver(store), scrapesrc.Options{Interval: 250 * time.Millisecond, Observer: first})
	if err := src.Subscribe(scrapesrc.Collector{}); err != nil {
		t.Fatal(err)
	}
	if err := src.UpdateEndpoints([]datalayer.EndpointID{"e"}); err != nil {
		t.Fatal(err)
	}
	if err := src.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Stop() }()
	select {
	case err := <-first:
		if err != nil {
			return nil, err
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s stub at %s was never scraped", mode, address)
	}
	ep, _ := store.Endpoint("e")
	m, _ := datalayer.Attribute[*datalayer.ServerMetrics](ep, datalayer.CollectorMetrics)
	return m, nil
}

// Every runtime local runs emulate has the resident adapters of its stub read as running by the gateway scrape
func TestAdapterModesMatchScrape(t *testing.T) {
	for _, mode := range stubModes {
		t.Run(string(mode), func(t *testing.T) {
			st, err := stub.New(stub.Config{Names: mode, Adapters: []stub.Adapter{{Name: "lora-a", Path: "/adapters/lora-a"}}})
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(st)
			t.Cleanup(func() {
				srv.Close()
				st.Close()
			})
			m, err := scrapeOnce(t, mode, strings.TrimPrefix(srv.URL, "http://"), map[string]string{datalayer.LabelKVCapacity: strconv.Itoa(st.KVCapacity())})
			if err != nil {
				t.Fatalf("scrape failed: %v", err)
			}
			if !m.AdapterActive("lora-a") {
				t.Fatalf("scrape reads running adapters %v", m.RunningAdapters)
			}
		})
	}
}

// An adapter trace starts on stubs of every emulated runtime with the adapter running on each
func TestLocalAdapterTraceByMode(t *testing.T) {
	for _, mode := range stubModes {
		t.Run(string(mode), func(t *testing.T) {
			g := newFakeGateway(t, "default")
			ctx := context.Background()
			l, err := StartLocal(ctx, g.api, localTrace(t), LocalOptions{Stubs: 2, Stub: fastStub(), Pool: "bench", Runtime: string(mode)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = l.Close(ctx) })
			pool, err := g.api.GetPool(ctx, connect.NewRequest(&v1.GetPoolRequest{Id: "bench"}))
			if err != nil {
				t.Fatal(err)
			}
			if pool.Msg.GetPool().GetSpec().GetRuntimeId() != string(mode) || len(pool.Msg.GetPool().GetStatus().GetEndpoints()) != 2 {
				t.Fatalf("pool %v", pool.Msg.GetPool())
			}
			for _, ep := range pool.Msg.GetPool().GetStatus().GetEndpoints() {
				if !slices.Equal(ep.GetMetrics().GetRunningAdapters(), []string{"lora-a"}) {
					t.Fatalf("endpoint %s runs adapters %v", ep.GetAddress(), ep.GetMetrics().GetRunningAdapters())
				}
			}
		})
	}
}

// Every stub of a llama.cpp local run declares its pool and completes a gateway scrape into full server metrics
func TestLocalLlamaCppRunScrapes(t *testing.T) {
	g := newFakeGateway(t, "default")
	ctx := context.Background()
	l, err := StartLocal(ctx, g.api, localTrace(t), LocalOptions{Stubs: 2, Stub: fastStub(), Pool: "bench", Runtime: "llamacpp"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close(ctx) })
	pool, err := g.api.GetPool(ctx, connect.NewRequest(&v1.GetPoolRequest{Id: "bench"}))
	if err != nil {
		t.Fatal(err)
	}
	const cells = 4096 * 16
	want := datalayer.ServerMetrics{BlockSize: 1, NumGPUBlocks: cells, MaxLoRA: 1, RunningAdapters: map[string]struct{}{"lora-a": {}}, WaitingAdapters: map[string]struct{}{}}
	statics := pool.Msg.GetPool().GetSpec().GetStaticEndpoints()
	if len(statics) != 2 {
		t.Fatalf("static endpoints %v", statics)
	}
	for _, se := range statics {
		if se.GetKvCapacity() != cells {
			t.Fatalf("%s declares a pool of %d tokens, the stub holds %d", se.GetAddress(), se.GetKvCapacity(), cells)
		}
		address := se.GetAddress()
		m, err := scrapeOnce(t, stub.NamesLlamaCpp, address, map[string]string{datalayer.LabelKVCapacity: strconv.FormatUint(uint64(se.GetKvCapacity()), 10)})
		if err != nil {
			t.Fatalf("scrape of %s failed: %v", address, err)
		}
		got := *m
		if got.UpdatedAt.IsZero() {
			t.Fatalf("%s sample carries no scrape time", address)
		}
		got.UpdatedAt = time.Time{}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s scraped %+v\nwant %+v", address, got, want)
		}
	}
}
