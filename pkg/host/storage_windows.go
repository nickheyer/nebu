//go:build windows

package host

import (
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/sys/windows"
)

// Reads capacity of the volume holding a path
func stat(path string) (*v1.Storage, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return nil, err
	}
	mount := path
	var volume [windows.MAX_PATH + 1]uint16
	if err := windows.GetVolumePathName(p, &volume[0], uint32(len(volume))); err == nil {
		mount = windows.UTF16ToString(volume[:])
	}
	fstype := ""
	var name [windows.MAX_PATH + 1]uint16
	if mp, err := windows.UTF16PtrFromString(mount); err == nil {
		if err := windows.GetVolumeInformation(mp, nil, 0, nil, nil, nil, &name[0], uint32(len(name))); err == nil {
			fstype = windows.UTF16ToString(name[:])
		}
	}
	return &v1.Storage{Path: mount, Filesystem: fstype, TotalBytes: total, FreeBytes: free}, nil
}
