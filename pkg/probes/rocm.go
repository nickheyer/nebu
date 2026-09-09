package probes

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
)

// AMD GPUs through rocm-smi, whose JSON keys one object per card with memory in bytes
type rocmSMI struct{}

func (rocmSMI) ID() string          { return "rocm-smi" }
func (rocmSMI) Description() string { return "AMD GPUs through rocm-smi" }
func (rocmSMI) Runs(string, string) bool {
	return true
}

func (p rocmSMI) Run(ctx context.Context) host.Result {
	out, res, ok := command(ctx, 0, "rocm-smi", "--showmeminfo", "vram", "--showproductname", "--showuniqueid", "--json")
	if !ok {
		return res
	}
	var cards map[string]map[string]any
	if err := json.Unmarshal(out, &cards); err != nil {
		return failed(fmt.Errorf("rocm-smi json: %w", err))
	}
	keys := make([]string, 0, len(cards))
	for k := range cards {
		if strings.HasPrefix(k, "card") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var devices []*v1.Device
	var pools []*v1.MemoryPool
	for _, key := range keys {
		card := cards[key]
		field := func(name string) string { return text.Scalar(card[name]) }
		total, err := bytesIn(field("VRAM Total Memory (B)"), "B")
		if err != nil {
			return failed(fmt.Errorf("%s: %w", key, err))
		}
		used, err := bytesIn(field("VRAM Total Used Memory (B)"), "B")
		if err != nil {
			return failed(fmt.Errorf("%s: %w", key, err))
		}
		free := uint64(0)
		if total > used {
			free = total - used
		}
		devices = append(devices, &v1.Device{
			Id:               key,
			Kind:             v1.DeviceKind_DEVICE_KIND_GPU,
			Vendor:           "amd",
			Name:             field("Card Series"),
			MemoryTotalBytes: total,
			MemoryFreeBytes:  free,
			Facts:            map[string]string{"index": strings.TrimPrefix(key, "card"), "unique_id": field("Unique ID"), "card_model": field("Card Model")},
		})
		pools = append(pools, &v1.MemoryPool{Id: key, Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: key, TotalBytes: total, FreeBytes: free})
	}
	return found(devices, pools, map[string]string{"amd.count": itoa(len(devices))}, rows(len(devices)))
}
