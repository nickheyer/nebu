package cli

import (
	"bytes"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Instance and formation views list every data parallel rank with its endpoint, mesh port, pid and health
func TestRanksListed(t *testing.T) {
	ranks := []*v1.InstanceRank{
		{Rank: 0, Endpoint: "http://127.0.0.1:8000", Pid: 41, Healthy: true},
		{Rank: 1, Endpoint: "http://127.0.0.1:8001", Pid: 42, Port: 9101},
	}
	cases := []struct {
		name   string
		render func(*bytes.Buffer)
		want   []string
		absent []string
	}{
		{name: "instance of two ranks", render: func(b *bytes.Buffer) {
			renderInstance(b, &v1.Instance{Id: "i1", Name: "m", Endpoint: "http://127.0.0.1:8000", Pid: 41, Ranks: ranks}, &v1.HostProfile{})
		}, want: []string{"RANKS", "RANK", "http://127.0.0.1:8001", "9101", "42"}},
		{name: "instance of one rank", render: func(b *bytes.Buffer) {
			renderInstance(b, &v1.Instance{Id: "i1", Name: "m", Endpoint: "http://127.0.0.1:8000", Pid: 41}, &v1.HostProfile{})
		}, absent: []string{"MESH PORT"}},
		{name: "formation seat of two ranks", render: func(b *bytes.Buffer) {
			renderFormation(b, &v1.Formation{Id: "f1", Name: "reps", Seats: []*v1.Seat{{Role: "replica", Rank: 1, NodeName: "b", Ranks: ranks}}})
		}, want: []string{"RANKS OF REPLICA1 ON B", "http://127.0.0.1:8001", "9101"}},
	}
	for _, c := range cases {
		var b bytes.Buffer
		c.render(&b)
		for _, w := range c.want {
			if !strings.Contains(b.String(), w) {
				t.Errorf("%s: output lacks %q:\n%s", c.name, w, b.String())
			}
		}
		for _, a := range c.absent {
			if strings.Contains(b.String(), a) {
				t.Errorf("%s: output holds %q:\n%s", c.name, a, b.String())
			}
		}
	}
	if got := endpointText(&v1.Instance{Endpoint: "http://h:1", Ranks: ranks}); got != "http://h:1 +1 ranks" {
		t.Fatalf("listing endpoint %q", got)
	}
}
