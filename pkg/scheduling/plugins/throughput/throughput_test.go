package throughput_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/internal/schedtest"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/scorer"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/throughput"
)

func TestThroughputMeasuresDecodeRate(t *testing.T) {
	ctx := context.Background()
	store := schedtest.NewStore()
	tp := throughput.New(store, 0.5)
	ep := schedtest.EP("e")
	t0 := time.Unix(0, 0)
	resp := func(tokens int, decode time.Duration) *framework.Response {
		return &framework.Response{Endpoint: ep, StartedAt: t0, FirstTokenAt: t0.Add(50 * time.Millisecond), FinishedAt: t0.Add(50*time.Millisecond + decode), OutputTokens: tokens}
	}
	steps := []struct {
		name string
		resp *framework.Response
		want float64
	}{
		{"first measurement taken whole", resp(101, time.Second), 100},
		{"next one averaged in by alpha", resp(201, time.Second), 150},
		{"a single token teaches nothing", resp(1, time.Second), 150},
		{"a failed response teaches nothing", &framework.Response{Endpoint: ep, FirstTokenAt: t0, FinishedAt: t0.Add(time.Second), OutputTokens: 11, Err: context.Canceled}, 150},
		{"no first token teaches nothing", &framework.Response{Endpoint: ep, FinishedAt: t0.Add(time.Second), OutputTokens: 11}, 150},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			tp.ResponseComplete(ctx, &framework.Request{ID: "r"}, st.resp)
			if got := tp.Rate("e"); math.Abs(got-st.want) > 1e-9 {
				t.Fatalf("rate %v, want %v", got, st.want)
			}
			if v, _ := store.Get("e", scorer.CollectorThroughput); v != st.want {
				t.Fatalf("stored %v, want %v", v, st.want)
			}
		})
	}
	tp.ResponseComplete(ctx, &framework.Request{ID: "r"}, nil)
	if tp.Rate("never") != 0 {
		t.Fatal("unmeasured endpoint has a rate")
	}
}
