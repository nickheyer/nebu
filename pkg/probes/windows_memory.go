package probes

import (
	"context"
	"fmt"
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Host memory from the operating system class, which counts in kilobytes
type windowsMemory struct{}

func (windowsMemory) ID() string          { return "windows-memory" }
func (windowsMemory) Description() string { return "Host memory from the operating system class" }
func (windowsMemory) Runs(os, arch string) bool {
	return on([]string{"windows"}, nil, os, arch)
}

func (p windowsMemory) Run(ctx context.Context) host.Result {
	out, res, ok := command(ctx, 20*time.Second, "powershell", "-NoProfile", "-NonInteractive", "-Command", "Get-CimInstance Win32_OperatingSystem | Select-Object TotalVisibleMemorySize,FreePhysicalMemory | Format-List")
	if !ok {
		return res
	}
	blocks := kvBlocks(out)
	if len(blocks) == 0 {
		return skipped("no operating system listed")
	}
	kv := blocks[0]
	total, err := bytesIn(kv["TotalVisibleMemorySize"], "kB")
	if err != nil {
		return failed(fmt.Errorf("TotalVisibleMemorySize: %w", err))
	}
	free, err := bytesIn(kv["FreePhysicalMemory"], "kB")
	if err != nil {
		return failed(fmt.Errorf("FreePhysicalMemory: %w", err))
	}
	pool := &v1.MemoryPool{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: total, FreeBytes: free}
	return found(nil, []*v1.MemoryPool{pool}, nil, rows(1))
}
