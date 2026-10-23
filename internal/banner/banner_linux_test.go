package banner

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/DataMata2/agent-freeze/internal/procfs"
)

// fdLinks creates root/<pid>/fd/<n> -> target for each entry of links.
func fdLinks(t *testing.T, root string, pid int, links map[int]string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid), "fd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, strconv.Itoa(n))); err != nil {
			t.Fatal(err)
		}
	}
}

// stubSameFile replaces the inode check for the rest of the test, so no
// real /dev path is ever stat'ed.
func stubSameFile(t *testing.T, f func(link, target string) bool) {
	t.Helper()
	old := sameFile
	sameFile = f
	t.Cleanup(func() { sameFile = old })
}

func TestTTY(t *testing.T) {
	stubSameFile(t, func(string, string) bool { return true })
	root := t.TempDir()
	fdLinks(t, root, 10, map[int]string{0: "/dev/pts/7", 1: "/dev/pts/7", 2: "/dev/pts/7"})
	fdLinks(t, root, 11, map[int]string{0: "socket:[123]", 1: "socket:[123]", 2: "socket:[124]"})
	fdLinks(t, root, 12, map[int]string{0: "pipe:[555]", 1: "pipe:[556]", 2: "/dev/null"})
	fdLinks(t, root, 13, map[int]string{0: "/dev/null", 1: "/dev/pts/3", 2: "/dev/pts/4"})
	fdLinks(t, root, 14, map[int]string{2: "/dev/tty2"})
	fdLinks(t, root, 17, map[int]string{0: "/dev/tty", 1: "/dev/pts/", 2: "/dev/pts/../sda"})
	fdLinks(t, root, 15, nil) // fd dir exists, no links
	if err := os.MkdirAll(filepath.Join(root, "16"), 0o755); err != nil {
		t.Fatal(err)
	}
	proc := procfs.FS{Root: root}
	for _, tc := range []struct {
		name string
		pid  int
		want string
	}{
		{"pts", 10, "/dev/pts/7"},
		{"socket", 11, ""},
		{"pipe", 12, ""},
		{"stdin not a tty, stdout pts", 13, "/dev/pts/3"},
		{"stderr tty", 14, "/dev/tty2"},
		{"empty fd dir", 15, ""},
		{"bare /dev/tty and unclean paths", 17, ""},
		{"no fd dir", 16, ""},
		{"no process", 99, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TTY(proc, tc.pid); got != tc.want {
				t.Errorf("TTY(%d) = %q, want %q", tc.pid, got, tc.want)
			}
		})
	}
}

func TestTTYOtherDevpts(t *testing.T) {
	root := t.TempDir()
	fdLinks(t, root, 10, map[int]string{0: "/dev/pts/3", 1: "/dev/pts/4", 2: "/dev/pts/4"})
	proc := procfs.FS{Root: root}
	var asked []string
	// fd/0 names a /dev/pts/3 of another devpts instance (a container).
	stubSameFile(t, func(link, target string) bool {
		asked = append(asked, filepath.Base(link)+"="+target)
		return filepath.Base(link) != "0"
	})
	if got := TTY(proc, 10); got != "/dev/pts/4" {
		t.Errorf("TTY = %q, want fall-through to fd/1 /dev/pts/4", got)
	}
	if want := []string{"0=/dev/pts/3", "1=/dev/pts/4"}; !slices.Equal(asked, want) {
		t.Errorf("sameFile calls = %q, want %q", asked, want)
	}
	stubSameFile(t, func(string, string) bool { return false })
	if got := TTY(proc, 10); got != "" {
		t.Errorf("TTY = %q, want \"\" when no fd is the named file", got)
	}
}
