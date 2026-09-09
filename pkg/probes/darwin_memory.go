package probes

import (
	"context"
	"fmt"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Host memory from sysctl on an Intel Mac
type darwinMemory struct{}

func (darwinMemory) ID() string          { return "darwin-memory" }
func (darwinMemory) Description() string { return "Host memory from sysctl on an Intel Mac" }
func (darwinMemory) Runs(os, arch string) bool {
	return on([]string{"darwin"}, []string{"amd64"}, os, arch)
}

func (p darwinMemory) Run(ctx context.Context) host.Result {
	total, free, res, ok := darwinPages(ctx)
	if !ok {
		return res
	}
	pool := &v1.MemoryPool{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: total, FreeBytes: free}
	return found(nil, []*v1.MemoryPool{pool}, map[string]string{"mem.total": itoa(int(total))}, rows(1))
}

// Reads memory size and the pages macOS can hand out: free pages sit near zero on macOS, so the
// speculative, purgeable, and file backed pages count as free too, since they are reclaimable
func darwinPages(ctx context.Context) (total, free uint64, res host.Result, ok bool) {
	out, res, ok := command(ctx, 0, "sysctl", "hw.memsize", "vm.page_free_count", "vm.page_speculative_count", "vm.page_purgeable_count", "vm.page_pageable_external_count", "vm.pagesize")
	if !ok {
		return 0, 0, res, false
	}
	blocks := kvBlocks(out)
	if len(blocks) == 0 {
		return 0, 0, skipped("sysctl printed nothing"), false
	}
	kv := blocks[0]
	read := func(key string) (uint64, error) {
		n, err := bytesIn(kv[key], "B")
		if err != nil {
			return 0, fmt.Errorf("%s: %w", key, err)
		}
		return n, nil
	}
	total, err := read("hw.memsize")
	if err != nil {
		return 0, 0, failed(err), false
	}
	pageSize, err := read("vm.pagesize")
	if err != nil {
		return 0, 0, failed(err), false
	}
	var pages uint64
	for _, key := range []string{"vm.page_free_count", "vm.page_speculative_count", "vm.page_purgeable_count", "vm.page_pageable_external_count"} {
		n, err := read(key)
		if err != nil {
			return 0, 0, failed(err), false
		}
		pages += n
	}
	return total, pages * pageSize, host.Result{}, true
}
