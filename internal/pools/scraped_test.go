package pools

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/stub"
)

// A llama.cpp stub published without its pool size makes Scraped false naming it and the error, a size clears it
func TestScrapeFailureCondition(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	st, err := stub.New(stub.Config{Model: "m", Names: stub.NamesLlamaCpp})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(st)
	t.Cleanup(func() {
		srv.Close()
		st.Close()
	})
	addr, err := datalayer.ParseAddress(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	owner := OwnerRef{Kind: OwnerInstance, ID: "l1"}
	ep := Published{InstanceID: "l1", NodeID: node, Address: addr, Ready: true}
	unsized := Membership{Model: llama, Runtime: "llamacpp", Protocol: datalayer.AppProtocolHTTP, Concurrency: 4}
	if err := m.Publish(ctx, owner, unsized, []Published{ep}); err != nil {
		t.Fatal(err)
	}
	id := string(PoolIDOf(llama, "llamacpp", ""))
	scraped := func() *v1.Condition {
		p, _ := m.Pool(id)
		return condition(p.GetStatus().GetConditions(), ConditionScraped)
	}
	waitUntil(t, "the Scraped condition to report the failing endpoint", func() bool {
		return scraped().GetStatus() == v1.ConditionStatus_CONDITION_STATUS_FALSE
	})
	c := scraped()
	if c.GetReason() != ReasonScrapeFailing || !strings.Contains(c.GetMessage(), "endpoint l1/"+itoa(addr.Port)) || !strings.Contains(c.GetMessage(), "kv-capacity label") {
		t.Fatalf("scraped condition %v", c)
	}
	sized := unsized
	sized.KVCapacity = uint32(st.KVCapacity())
	if err := m.Publish(ctx, owner, sized, []Published{ep}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the Scraped condition to clear", func() bool {
		return scraped().GetStatus() == v1.ConditionStatus_CONDITION_STATUS_TRUE
	})
	if c := scraped(); c.GetReason() != ReasonScraped {
		t.Fatalf("scraped condition after the size %v", c)
	}
}

// A pool whose runtime declares no metrics reports nothing scraped
func TestNotScrapedCondition(t *testing.T) {
	m, _, _ := newManager(t)
	p, err := m.PutPool(context.Background(), "plain", &v1.PoolSpec{Model: "m", TargetPorts: []uint32{8000}, StaticEndpoints: []*v1.StaticEndpoint{{Address: "10.0.0.1:8000"}}})
	if err != nil {
		t.Fatal(err)
	}
	if c := condition(p.GetStatus().GetConditions(), ConditionScraped); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE || c.GetReason() != ReasonNotScraped {
		t.Fatalf("scraped condition %v", c)
	}
}
