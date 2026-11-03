package requestcontrol

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

func FuzzRoutingHeaders(f *testing.F) {
	f.Add("3", "250", "40", "10.0.0.1:8000, [::1]:9")
	f.Add("x", "-1", "1.5", "nohost")
	f.Add("", "", "", ",,")
	f.Fuzz(func(t *testing.T, priority, ttft, tpot, subset string) {
		h := http.Header{}
		h.Set(HeaderObjective, priority)
		h.Set(HeaderTTFTObjective, ttft)
		h.Set(HeaderTPOTObjective, tpot)
		h.Set(HeaderSubset, subset)
		obj, err := parseObjective(h)
		if err != nil {
			if !errors.Is(err, ErrInvalidHeader) {
				t.Fatalf("objective err %v is not ErrInvalidHeader", err)
			}
		} else if p := strings.TrimSpace(h.Get(HeaderObjective)); p != "" {
			if want, _ := strconv.Atoi(p); obj.Priority != want {
				t.Fatalf("priority %d from %q", obj.Priority, p)
			}
		}
		if obj.TTFT < 0 || obj.TPOT < 0 {
			t.Fatalf("negative objective %+v", obj)
		}
		addrs, set, err := parseSubset(h)
		if !set {
			t.Fatal("a present subset header was not marked set")
		}
		if err != nil {
			if !errors.Is(err, ErrInvalidHeader) {
				t.Fatalf("subset err %v is not ErrInvalidHeader", err)
			}
			return
		}
		for _, a := range addrs {
			back, err := datalayer.ParseAddress(a.String())
			if err != nil || back != a {
				t.Fatalf("address %v does not round trip, got %v %v", a, back, err)
			}
		}
	})
}

// A lifecycle plugin whose hooks do nothing, for measuring the director alone
type quietHook struct{}

func (quietHook) TypedName() framework.TypedName                                            { return framework.TypedName{Type: "quiet"} }
func (quietHook) PreRequest(context.Context, *framework.Request, *framework.Result)         {}
func (quietHook) ResponseComplete(context.Context, *framework.Request, *framework.Response) {}

func BenchmarkDecide(b *testing.B) {
	e := newEnv(b, func(d *Deps) { d.Lifecycle = []framework.Plugin{quietHook{}} })
	body := `{"model":"llama","stream":true,"max_tokens":64,"messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"Summarize the plan in three bullet points please."}]}`
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			dec, err := e.dir.Decide(context.Background(), inbound(b, body, HeaderFairnessID, "tenant"))
			if err != nil {
				b.Error(err)
				return
			}
			e.dir.ResponseComplete(context.Background(), dec, &framework.Response{})
		}
	})
}
