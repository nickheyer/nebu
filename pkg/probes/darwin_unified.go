package probes

import (
	"context"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Unified memory from sysctl on Apple silicon, one pool the CPU and GPU share
type darwinUnified struct{}

func (darwinUnified) ID() string          { return "darwin-unified" }
func (darwinUnified) Description() string { return "Unified memory from sysctl on Apple silicon" }
func (darwinUnified) Runs(os, arch string) bool {
	return on([]string{"darwin"}, []string{"arm64"}, os, arch)
}

func (p darwinUnified) Run(ctx context.Context) host.Result {
	total, free, res, ok := darwinPages(ctx)
	if !ok {
		return res
	}
	pool := &v1.MemoryPool{Id: "unified", Kind: v1.PoolKind_POOL_KIND_UNIFIED, TotalBytes: total, FreeBytes: free}
	return found(nil, []*v1.MemoryPool{pool}, map[string]string{"mem.total": itoa(int(total))}, rows(1))
}
