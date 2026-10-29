//go:build linux || darwin

// Package banner writes a "frozen" notice into a frozen agent's terminal:
// a reverse-video line on the bottom row and the window title. A frozen
// agent otherwise looks hung, since keystrokes queue without being echoed.
package banner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/DataMata2/agent-freeze/internal/prog"
)

const (
	saveCursor    = "\x1b7"
	bottomRow     = "\x1b[999;1H"
	clearLine     = "\x1b[2K"
	reverseVideo  = "\x1b[7m"
	resetAttrs    = "\x1b[0m"
	restoreCursor = "\x1b8"
	titleStart    = "\x1b]2;"
	titleEnd      = "\a"
	pushTitle     = "\x1b[22;2t" // XTWINOPS: save the window title on the stack
	popTitle      = "\x1b[23;2t" // XTWINOPS: restore it

	frozenText  = " ❄ frozen by %s at %s — keystrokes are queued until thaw · don't close this window "
	frozenTitle = "❄ FROZEN — "
	clockLayout = "15:04"
	ptsPrefix   = "/dev/pts/"
	ttyPrefix   = "/dev/tty"
	// abortTail ends whatever a partial write left open: the title (BEL),
	// reverse video and the saved cursor.
	abortTail = titleEnd + resetAttrs + restoreCursor
)

// Seams for tests; production uses the real kernel.
var (
	// termWidth returns the column count of the terminal open on fd, or 0
	// if unknown.
	termWidth = func(fd int) int {
		ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
		if err != nil {
			return 0
		}
		return int(ws.Col)
	}
	rawWrite = syscall.Write
)

// IsTTYPath reports whether p is a clean /dev/pts/N or /dev/ttyX path.
// "/dev/tty" itself is excluded: it means the caller's own terminal.
func IsTTYPath(p string) bool {
	return (strings.HasPrefix(p, ptsPrefix) && len(p) > len(ptsPrefix) ||
		strings.HasPrefix(p, ttyPrefix) && len(p) > len(ttyPrefix)) && filepath.Clean(p) == p
}

// CheckTTY refuses any path that is not a clean /dev/pts/* or /dev/tty*
// path naming a character device.
func CheckTTY(tty string) error {
	if !IsTTYPath(tty) {
		return fmt.Errorf("refusing to write to %q: not a terminal path", tty)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(tty, &st); err != nil {
		return &os.PathError{Op: "stat", Path: tty, Err: err}
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFCHR {
		return fmt.Errorf("refusing to write to %q: not a character device", tty)
	}
	return nil
}

// ShowFrozen writes the freeze banner (bottom row, reverse video, local
// time of at) and sets the window title to "❄ FROZEN — name". It writes
// only to /dev/pts/* or /dev/tty* character devices and never blocks: if
// the terminal is not accepting output, it gives up and returns nil.
func ShowFrozen(tty, name string, at time.Time) error {
	if err := CheckTTY(tty); err != nil {
		return err
	}
	return showFrozen(tty, name, at)
}

// ClearFrozen erases the bottom row written by ShowFrozen and sets the
// window title back to name. The same path rules as ShowFrozen apply.
func ClearFrozen(tty, name string) error {
	if err := CheckTTY(tty); err != nil {
		return err
	}
	return clearFrozen(tty, name)
}

func showFrozen(path, name string, at time.Time) error {
	text := fmt.Sprintf(frozenText, sanitize(prog.Name), at.Local().Format(clockLayout))
	return write(path, func(cols int) string {
		if cols > 0 {
			text = truncate(text, cols-1)
		}
		return saveCursor + bottomRow + clearLine + reverseVideo + text + resetAttrs + restoreCursor +
			pushTitle + titleStart + frozenTitle + sanitize(name) + titleEnd
	})
}

func clearFrozen(path, name string) error {
	return write(path, func(int) string {
		return saveCursor + bottomRow + clearLine + restoreCursor + titleStart + sanitize(name) + titleEnd + popTitle
	})
}

// truncate cuts s to at most cells terminal cells, so the banner never
// wraps and scrolls the screen. ❄ counts as two cells (emoji presentation).
func truncate(s string, cells int) string {
	used := 0
	for i, r := range s {
		w := 1
		if r == '❄' {
			w = 2
		}
		if used+w > cells {
			return s[:i]
		}
		used += w
	}
	return s
}

// sanitize drops control characters (C0, DEL, C1) and invalid UTF-8 from s,
// so a name cannot end the title sequence or inject escape sequences.
func sanitize(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		if r == utf8.RuneError && n == 1 || unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// write writes s to path without blocking.
//
// It uses raw syscalls rather than os.File, as package cgroup does: a tty is
// pollable, so os.File would register it with Go's netpoller and turn a
// full terminal (EAGAIN) into an indefinite wait. A banner must never stall
// a freeze, so EAGAIN gives up silently; if part of s was already written,
// abortTail is sent (best effort) so no title or reverse video stays open.
// build gets the terminal width (0 if unknown) and returns the bytes.
func write(path string, build func(cols int) string) error {
	flag := syscall.O_WRONLY | syscall.O_NOCTTY | syscall.O_NONBLOCK | syscall.O_CLOEXEC
	fd, err := retryEINTR(func() (int, error) { return syscall.Open(path, flag, 0) })
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer func() { _ = syscall.Close(fd) }()
	b := []byte(build(termWidth(fd)))
	written := false
	for len(b) > 0 {
		n, err := retryEINTR(func() (int, error) { return rawWrite(fd, b) })
		if errors.Is(err, syscall.EAGAIN) {
			if written {
				_, _ = retryEINTR(func() (int, error) { return rawWrite(fd, []byte(abortTail)) })
			}
			return nil
		}
		if err != nil {
			return &os.PathError{Op: "write", Path: path, Err: err}
		}
		if n <= 0 {
			return &os.PathError{Op: "write", Path: path, Err: io.ErrShortWrite}
		}
		written = true
		b = b[n:]
	}
	return nil
}

func retryEINTR(f func() (int, error)) (int, error) {
	for {
		n, err := f()
		if err != syscall.EINTR {
			return n, err
		}
	}
}
