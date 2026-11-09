package pools

import (
	"slices"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestSeatIDs(t *testing.T) {
	tests := []struct {
		name string
		f    *v1.Formation
		want []string
	}{
		{name: "no formation"},
		{name: "unlaunched seats", f: &v1.Formation{Seats: []*v1.Seat{{Role: "prefill"}, {Role: "decode"}}}},
		{name: "launched in seat order", f: &v1.Formation{Seats: []*v1.Seat{{InstanceId: "b"}, {}, {InstanceId: "a"}}}, want: []string{"b", "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SeatIDs(tt.f); !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
