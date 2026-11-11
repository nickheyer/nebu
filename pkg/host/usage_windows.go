//go:build windows

package host

import (
	"io/fs"
	"path/filepath"
	"slices"

	"golang.org/x/sys/windows"
)

// Bytes the files under a directory hold, a file with several links once, the skipped directories left out
func usage(root string, skip []string) uint64 {
	seen := map[[3]uint32]struct{}{}
	var total uint64
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && slices.Contains(skip, path) {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		if id, links := fileID(path); links > 1 {
			if _, dup := seen[id]; dup {
				return nil
			}
			seen[id] = struct{}{}
		}
		total += uint64(info.Size())
		return nil
	})
	return total
}

// The volume and index that name a file whatever its path, with how many paths link to it
func fileID(path string) ([3]uint32, uint32) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return [3]uint32{}, 0
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return [3]uint32{}, 0
	}
	defer windows.CloseHandle(h)
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &fi); err != nil {
		return [3]uint32{}, 0
	}
	return [3]uint32{fi.VolumeSerialNumber, fi.FileIndexHigh, fi.FileIndexLow}, fi.NumberOfLinks
}
