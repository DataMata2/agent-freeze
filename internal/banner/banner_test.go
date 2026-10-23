//go:build linux || darwin

package banner

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DataMata2/agent-freeze/internal/prog"
)

func tempTTY(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tty")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestShowFrozenBytes(t *testing.T) {
	p := tempTTY(t)
	at := time.Date(2026, 10, 21, 9, 5, 0, 0, time.Local)
	if err := showFrozen(p, "fix-login", at); err != nil {
		t.Fatal(err)
	}
	want := "\x1b7\x1b[999;1H\x1b[2K\x1b[7m" +
		" ❄ frozen by afz at 09:05 — keystrokes are queued until thaw · don't close this window " +
		"\x1b[0m\x1b8" +
		"\x1b[22;2t\x1b]2;❄ FROZEN — fix-login\a"
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("bytes = %q\nwant    %q", got, want)
	}
}

func TestShowFrozenTruncatesToWidth(t *testing.T) {
	at := time.Date(2026, 10, 21, 9, 5, 0, 0, time.Local)
	old := termWidth
	t.Cleanup(func() { termWidth = old })
	for _, tc := range []struct {
		cols int
		text string
	}{
		{30, " ❄ frozen by afz at 09:05 — "}, // 29 cells, ❄ counted as 2
		{3, " "},                             // ❄ does not fit in 2 cells
		{1, ""},
	} {
		termWidth = func(int) int { return tc.cols }
		p := tempTTY(t)
		if err := showFrozen(p, "w", at); err != nil {
			t.Fatal(err)
		}
		want := "\x1b7\x1b[999;1H\x1b[2K\x1b[7m" + tc.text + "\x1b[0m\x1b8\x1b[22;2t\x1b]2;❄ FROZEN — w\a"
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("cols %d: bytes = %q\nwant    %q", tc.cols, got, want)
		}
	}
}

func TestClearFrozenBytes(t *testing.T) {
	p := tempTTY(t)
	if err := clearFrozen(p, "fix-login"); err != nil {
		t.Fatal(err)
	}
	want := "\x1b7\x1b[999;1H\x1b[2K\x1b8\x1b]2;fix-login\a\x1b[23;2t"
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("bytes = %q\nwant    %q", got, want)
	}
}

func TestTitleStripsControlCharacters(t *testing.T) {
	p := tempTTY(t)
	if err := clearFrozen(p, "a\x1b]0;evil\a\x9b\u009bb\nc"); err != nil {
		t.Fatal(err)
	}
	want := "\x1b7\x1b[999;1H\x1b[2K\x1b8\x1b]2;a]0;evilbc\a\x1b[23;2t"
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("bytes = %q\nwant    %q", got, want)
	}
}

func TestExportedRejectNonTTYPaths(t *testing.T) {
	p := tempTTY(t)
	at := time.Date(2026, 10, 21, 9, 5, 0, 0, time.Local)
	for _, bad := range []string{p, "", "/dev/null", "/dev/pts", "/dev/pts/", "/dev/tty", "/dev/pts/../sda", "relative/dev/pts/1", "/tmp/dev/tty1"} {
		if err := ShowFrozen(bad, "x", at); err == nil {
			t.Errorf("ShowFrozen(%q) = nil, want error", bad)
		}
		if err := ClearFrozen(bad, "x"); err == nil {
			t.Errorf("ClearFrozen(%q) = nil, want error", bad)
		}
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("temp file written: %q", got)
	}
}

func TestWriteMissingPathIsError(t *testing.T) {
	if err := clearFrozen(filepath.Join(t.TempDir(), "nope"), "x"); err == nil {
		t.Error("write to missing path = nil, want error")
	}
}

// TestWriteFullGivesUp checks that a write that would block (EAGAIN) returns
// nil at once instead of waiting: a FIFO whose reader never reads stands in
// for a terminal that is not accepting output.
func TestWriteFullGivesUp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	rfd, err := syscall.Open(p, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(rfd) })
	done := make(chan error, 1)
	go func() { done <- write(p, func(int) string { return strings.Repeat("x", 4<<20) }) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("write to full FIFO = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("write to full FIFO blocked")
	}
}

// TestPartialWriteClosesSequences checks that an EAGAIN after part of the
// banner was written sends BEL, SGR reset and restore cursor, and that an
// EAGAIN before anything was written sends nothing.
func TestPartialWriteClosesSequences(t *testing.T) {
	old := rawWrite
	t.Cleanup(func() { rawWrite = old })
	at := time.Date(2026, 10, 21, 9, 5, 0, 0, time.Local)
	for _, first := range []int{20, 0} {
		calls := 0
		rawWrite = func(fd int, b []byte) (int, error) {
			calls++
			switch calls {
			case 1:
				if first == 0 {
					return 0, syscall.EAGAIN
				}
				return syscall.Write(fd, b[:first])
			case 2:
				return 0, syscall.EAGAIN
			}
			return syscall.Write(fd, b)
		}
		p := tempTTY(t)
		if err := showFrozen(p, "w", at); err != nil {
			t.Fatalf("first %d: %v", first, err)
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		want := ""
		if first > 0 {
			want = "\x1b7\x1b[999;1H\x1b[2K\x1b[7m ❄ fro"[:first] + "\a\x1b[0m\x1b8"
		}
		if string(got) != want {
			t.Errorf("first %d: bytes = %q, want %q", first, got, want)
		}
	}
}

func TestBannerStripsControlCharactersFromTheCommandName(t *testing.T) {
	defer func(old string) { prog.Name = old }(prog.Name)
	prog.Name = "chill\x1b]2;owned\a"
	p := tempTTY(t)
	if err := showFrozen(p, "x", time.Now()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if out := string(b); strings.Contains(out, "\x1b]2;owned") || !strings.Contains(out, "frozen by chill]2;owned at") {
		t.Fatalf("banner %q", out)
	}
}
