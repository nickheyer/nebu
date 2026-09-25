//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package links

// These systems publish no interface speed to a user process, so the measured bandwidth stands
// as the link's figure
func speedOf(string) uint64 { return 0 }

// The RDMA transports nebu drives bind through the Linux class tree, so a link here is sockets
// and the class reflects that
func rdmaOf(string) string { return "" }
