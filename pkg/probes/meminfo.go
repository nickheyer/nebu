package probes

import (
	"context"
	"fmt"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Host memory from procfs, MemAvailable being what the kernel would hand a new process
type meminfo struct{}

func (meminfo) ID() string          { return "meminfo" }
func (meminfo) Description() string { return "Host memory from procfs" }
func (meminfo) Runs(os, arch string) bool {
	return on([]string{"linux"}, nil, os, arch)
}

func (p meminfo) Run(context.Context) host.Result {
	data, res, ok := file("/proc/meminfo")
	if !ok {
		return res
	}
	blocks := kvBlocks(data)
	if len(blocks) == 0 {
		return failed(fmt.Errorf("/proc/meminfo is empty"))
	}
	kv := blocks[0]
	total, err := bytesIn(kv["MemTotal"], "kB")
	if err != nil {
		return failed(fmt.Errorf("MemTotal: %w", err))
	}
	free, err := bytesIn(kv["MemAvailable"], "kB")
	if err != nil {
		return failed(fmt.Errorf("MemAvailable: %w", err))
	}
	pool := &v1.MemoryPool{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: total, FreeBytes: free}
	// Memory totals belong to the pool. Huge page counts remain host facts.
	facts := map[string]string{}
	if v, ok := kv["HugePages_Total"]; ok {
		facts["mem.hugepages_total"] = v
	}
	return found(nil, []*v1.MemoryPool{pool}, facts, rows(1))
}
