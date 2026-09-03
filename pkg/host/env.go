package host

import (
	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Builds the expression environment for a profile
func Env(p *v1.HostProfile) map[string]any {
	devices := make([]any, 0, len(p.GetDevices()))
	for _, d := range p.GetDevices() {
		devices = append(devices, map[string]any{
			"id":                 d.GetId(),
			"kind":               eval.EnumShort(d.GetKind()),
			"vendor":             d.GetVendor(),
			"name":               d.GetName(),
			"memory_total_bytes": float64(d.GetMemoryTotalBytes()),
			"memory_free_bytes":  float64(d.GetMemoryFreeBytes()),
			"facts":              stringMap(d.GetFacts()),
		})
	}
	pools := make([]any, 0, len(p.GetPools()))
	for _, pl := range p.GetPools() {
		pools = append(pools, map[string]any{
			"id":          pl.GetId(),
			"kind":        eval.EnumShort(pl.GetKind()),
			"device_id":   pl.GetDeviceId(),
			"total_bytes": float64(pl.GetTotalBytes()),
			"free_bytes":  float64(pl.GetFreeBytes()),
		})
	}
	storage := make([]any, 0, len(p.GetStorage()))
	for _, s := range p.GetStorage() {
		uses := make([]any, 0, len(s.GetUses()))
		for _, u := range s.GetUses() {
			uses = append(uses, u)
		}
		storage = append(storage, map[string]any{
			"path":        s.GetPath(),
			"filesystem":  s.GetFilesystem(),
			"total_bytes": float64(s.GetTotalBytes()),
			"free_bytes":  float64(s.GetFreeBytes()),
			"uses":        uses,
		})
	}
	return map[string]any{
		"hostname": p.GetHostname(),
		"os":       p.GetOs(),
		"arch":     p.GetArch(),
		"facts":    stringMap(p.GetFacts()),
		"devices":  devices,
		"pools":    pools,
		"storage":  storage,
	}
}

func stringMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
