//go:build unix

package host

import (
	"bufio"
	"os"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/sys/unix"
)

// Reads capacity of the filesystem holding a path, keyed by mount
func stat(path string) (*v1.Storage, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return nil, err
	}
	mount, fstype := mountOf(path)
	bsize := uint64(fs.Bsize)
	return &v1.Storage{
		Path:       mount,
		Filesystem: fstype,
		TotalBytes: fs.Blocks * bsize,
		FreeBytes:  fs.Bavail * bsize,
	}, nil
}

// Finds the longest mount point containing a path
func mountOf(path string) (string, string) {
	best, bestType := path, ""
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return best, bestType
	}
	defer f.Close()
	found := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		mount := fields[1]
		inside := mount == "/" || path == mount || strings.HasPrefix(path, mount+"/")
		if inside && (!found || len(mount) > len(best)) {
			best, bestType, found = mount, fields[2], true
		}
	}
	return best, bestType
}
