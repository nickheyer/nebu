//go:build !windows

package host

import (
	"io/fs"
	"path/filepath"
	"slices"
	"syscall"
)

// Disk bytes the files under a directory take on its own filesystem, each inode once, the skipped directories left out
func usage(root string, skip []string) uint64 {
	var dev uint64
	seen := map[uint64]struct{}{}
	var total uint64
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != root && slices.Contains(skip, path) {
			return fs.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		if path == root {
			dev = uint64(st.Dev)
		}
		if uint64(st.Dev) != dev {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if _, dup := seen[st.Ino]; dup {
			return nil
		}
		seen[st.Ino] = struct{}{}
		total += uint64(st.Blocks) * 512
		return nil
	})
	return total
}
