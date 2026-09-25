package links_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/mesh/links"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// The class cutoffs follow the measured numbers, an RDMA device on both ends making a fabric
func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		link *v1.Link
		peer string
		want v1.LinkClass
	}{
		{"fabric", &v1.Link{RttUs: 30, StreamBytesPerSecond: 12e9 / 8, AggregateBytesPerSecond: 190e9 / 8, RdmaDevice: "mlx5_0"}, "mlx5_0", v1.LinkClass_LINK_CLASS_FABRIC},
		{"rdma one end", &v1.Link{RttUs: 30, StreamBytesPerSecond: 12e9 / 8, AggregateBytesPerSecond: 190e9 / 8, RdmaDevice: "mlx5_0"}, "", v1.LinkClass_LINK_CLASS_FAST},
		{"fast", &v1.Link{RttUs: 210, StreamBytesPerSecond: 9.4e9 / 8, AggregateBytesPerSecond: 9.4e9 / 8}, "", v1.LinkClass_LINK_CLASS_LAN},
		{"ten gig", &v1.Link{RttUs: 210, StreamBytesPerSecond: 11e9 / 8, AggregateBytesPerSecond: 11e9 / 8}, "", v1.LinkClass_LINK_CLASS_FAST},
		{"lan", &v1.Link{RttUs: 180, StreamBytesPerSecond: 2.3e9 / 8, AggregateBytesPerSecond: 2.3e9 / 8}, "", v1.LinkClass_LINK_CLASS_LAN},
		{"slow rtt", &v1.Link{RttUs: 5000, StreamBytesPerSecond: 2.3e9 / 8, AggregateBytesPerSecond: 2.3e9 / 8}, "", v1.LinkClass_LINK_CLASS_SLOW},
		{"slow rate", &v1.Link{RttUs: 180, StreamBytesPerSecond: 90e6 / 8, AggregateBytesPerSecond: 90e6 / 8}, "", v1.LinkClass_LINK_CLASS_SLOW},
	}
	for _, c := range cases {
		got, detail := links.Classify(c.link, c.peer)
		if got != c.want {
			t.Errorf("%s: class %v, want %v: %s", c.name, got, c.want, detail)
		}
		if detail == "" {
			t.Errorf("%s: a class comes with the reason", c.name)
		}
	}
}

// A stream server for the download measurement
type streamer struct {
	nebuv1connect.UnimplementedMeshServiceHandler
}

func (streamer) Stream(ctx context.Context, req *connect.Request[v1.StreamRequest], stream *connect.ServerStream[v1.StreamResponse]) error {
	return links.Stream(ctx, req.Msg.GetBytes(), stream.Send)
}

// A measurement over loopback pings, uploads, downloads, and reads the interface the route leaves through
func TestMeasureOverLoopback(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewMeshServiceHandler(streamer{}))
	mux.HandleFunc(links.SinkPath, links.Sink)
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.Start()
	defer srv.Close()
	p := &links.Prober{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// A last run under 100 Mb/s picks the smaller bandwidth run.
	last := &v1.Link{StreamBytesPerSecond: 1}
	link, err := p.Measure(ctx, "a", links.Target{ID: "b", Address: srv.Listener.Addr().String(), Header: http.Header{}}, last, false)
	if err != nil {
		t.Fatal(err)
	}
	if link.GetRttUs() == 0 || link.GetStreamBytesPerSecond() == 0 || link.GetAggregateBytesPerSecond() < link.GetStreamBytesPerSecond() {
		t.Fatalf("link %v", link)
	}
	if link.GetInterface() == "" || !link.GetSubnet() {
		t.Fatalf("the loopback route leaves through an interface on its own subnet: %v", link)
	}
	if link.GetClass() == v1.LinkClass_LINK_CLASS_UNSPECIFIED || link.GetDetail() == "" {
		t.Fatalf("class %v detail %q", link.GetClass(), link.GetDetail())
	}
	held, err := p.Measure(ctx, "a", links.Target{ID: "b", Address: srv.Listener.Addr().String(), Header: http.Header{}}, link, true)
	if err != nil {
		t.Fatal(err)
	}
	if !held.GetBandwidthHeld() || held.GetStreamBytesPerSecond() != link.GetStreamBytesPerSecond() {
		t.Fatalf("a held run keeps the last bandwidth: %v", held)
	}
}
