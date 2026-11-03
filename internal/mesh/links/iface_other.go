//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package links

// No interface speed reaches a user process here, so measured bandwidth stands alone
func speedOf(string) uint64 { return 0 }

// No RDMA device binds here since the transports nebu drives use the Linux class tree
func rdmaOf(string) string { return "" }
