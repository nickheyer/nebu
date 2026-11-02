package gateway

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A route carries the sequence slots its instance's params declare, nothing when they do not
func TestRouteCarriesSequenceSlots(t *testing.T) {
	for in, want := range map[string]uint32{"": 0, "2": 2, "x": 0, "0": 0} {
		if got := sequenceSlots(map[string]string{"n_parallel": in}); got != want {
			t.Errorf("n_parallel %q: %d slots, want %d", in, got, want)
		}
	}
	_, table := newGateway(t)
	r := table.Serve("m", &v1.Instance{Id: "i1", Endpoint: "http://127.0.0.1:1", Repo: "r", Group: "g", Name: "m", Params: map[string]string{"n_parallel": "2"}}, v1.ApiFlavor_API_FLAVOR_OPENAI, "", nil, nil)
	if r.GetSlots() != 2 {
		t.Fatalf("served route %+v", r)
	}
	r = table.Serve("m", &v1.Instance{Id: "i1", Endpoint: "http://127.0.0.1:1", Repo: "r", Group: "g", Name: "m"}, v1.ApiFlavor_API_FLAVOR_OPENAI, "", nil, nil)
	if r.GetSlots() != 0 {
		t.Fatalf("a run without n_parallel keeps %d slots", r.GetSlots())
	}
	// Every acquisition says how many requests were already in flight ahead of it
	for want := uint32(0); want < 3; want++ {
		_, _, ahead, _, err := table.Acquire("m", true)
		if err != nil || ahead != want {
			t.Fatalf("acquisition %d: ahead %d %v", want, ahead, err)
		}
	}
}

// A request past the slots is traced as queued behind the ones holding them and waits without the header timeout
func TestQueuedPastTheSlotsIsTraced(t *testing.T) {
	first, second := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	model := newSeat(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		switch calls.Add(1) {
		case 1:
			<-first
		case 2:
			<-second
		}
		chatAnswer(w, "ok")
	})
	g, table := newGateway(t)
	table.SetDefaults(&v1.Policy{UpstreamTimeoutMs: 300})
	table.Serve("m", &v1.Instance{Id: "i1", Endpoint: model.URL, Repo: "r", Group: "g", Name: "m", Params: map[string]string{"n_parallel": "1"}}, v1.ApiFlavor_API_FLAVOR_OPENAI, "", nil, nil)
	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)
	var wg sync.WaitGroup
	wg.Add(1)
	var holder *v1.Trace
	go func() {
		defer wg.Done()
		_, holder = ask(t, g, srv.URL, http.MethodPost, chat.OpenAIChat, oaiBody(false, ""), nil)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for table.InFlight("i1") < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// The holder answers inside the header timeout, the queued request only well past it
	time.AfterFunc(100*time.Millisecond, func() { close(first) })
	time.AfterFunc(600*time.Millisecond, func() { close(second) })
	r, queued := ask(t, g, srv.URL, http.MethodPost, chat.OpenAIChat, oaiBody(false, ""), nil)
	wg.Wait()
	if r.status != http.StatusOK || queued.GetQueuedBehind() != 1 || queued.GetError() != "" {
		t.Fatalf("queued request %d %s trace %+v", r.status, r.body, queued)
	}
	if holder.GetQueuedBehind() != 0 || holder.GetStatus() != http.StatusOK {
		t.Fatalf("the request that took the slot %+v", holder)
	}
}
