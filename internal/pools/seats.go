package pools

import v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"

// The instance ids of a formation's launched seats in seat order, none for no formation
func SeatIDs(f *v1.Formation) []string {
	var out []string
	for _, s := range f.GetSeats() {
		if s.GetInstanceId() != "" {
			out = append(out, s.GetInstanceId())
		}
	}
	return out
}
