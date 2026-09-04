//go:build darwin || freebsd

package host

import (
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/sys/unix"
)

// Reads capacity of the filesystem holding a path, the statfs record naming its mount
func stat(path string) (*v1.Storage, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return nil, err
	}
	bsize := uint64(fs.Bsize)
	return &v1.Storage{
		Path:       unix.ByteSliceToString(fs.Mntonname[:]),
		Filesystem: unix.ByteSliceToString(fs.Fstypename[:]),
		TotalBytes: uint64(fs.Blocks) * bsize,
		FreeBytes:  uint64(fs.Bavail) * bsize,
	}, nil
}
