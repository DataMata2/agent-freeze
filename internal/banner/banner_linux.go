package banner

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/DataMata2/agent-freeze/internal/procfs"
)

const maxFDToCheck = 2

// sameFile reports whether the fd magic link and the path it names are
// the same inode. A process in another devpts instance (a container)
// has a /dev/pts/N that is not the host's /dev/pts/N. Tests replace it.
var sameFile = func(link, target string) bool {
	a, err := os.Stat(link) // follows the magic link to the real inode
	if err != nil {
		return false
	}
	b, err := os.Stat(target)
	return err == nil && os.SameFile(a, b) // compares Dev and Ino
}

// TTY returns the terminal device of pid: the target of the first of
// /proc/<pid>/fd/0, 1 and 2 that is a /dev/pts/* or /dev/tty* path and is
// the very file that fd has open (same device and inode), or "".
func TTY(proc procfs.FS, pid int) string {
	dir := filepath.Join(proc.Root, strconv.Itoa(pid), "fd")
	for fd := 0; fd <= maxFDToCheck; fd++ {
		link := filepath.Join(dir, strconv.Itoa(fd))
		target, err := os.Readlink(link)
		if err == nil && isTTYPath(target) && sameFile(link, target) {
			return target
		}
	}
	return ""
}
