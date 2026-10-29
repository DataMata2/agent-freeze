package auto

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"golang.org/x/sys/unix"

	"github.com/DataMata2/agent-freeze/internal/banner"
)

// inputPollMillis bounds how long a new tty set waits for the watcher to take it.
const inputPollMillis = 1000

// ttyPoller is the kernel surface of the input watcher; tests replace it.
type ttyPoller interface {
	open(path string) (int, error)
	poll(fds []unix.PollFd, timeoutMillis int) (int, error)
	unread(fd int) (int, error) // TIOCINQ: bytes typed and not read yet
	close(fd int)
}

type sysPoller struct{}

// open opens a terminal for poll and TIOCINQ only: it never becomes the
// controlling terminal, never blocks, and is never read from.
func (sysPoller) open(path string) (int, error) {
	if !ttyPath(path) {
		return -1, fmt.Errorf("refusing to open %q: not a terminal path", path)
	}
	if err := banner.CheckTTY(path); err != nil {
		return -1, err
	}
	return unix.Open(path, unix.O_RDONLY|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
}

func (sysPoller) poll(fds []unix.PollFd, ms int) (int, error) { return unix.Poll(fds, ms) }
func (sysPoller) unread(fd int) (int, error)                  { return unix.IoctlGetInt(fd, unix.TIOCINQ) }
func (sysPoller) close(fd int)                                { unix.Close(fd) }

var inputPoller ttyPoller = sysPoller{}

// WatchInput watches the ttys Run offers and reports one that has typed,
// unread input. Poll and TIOCINQ leave the bytes for the agent to read.
func WatchInput(ctx context.Context) InputWatch {
	sets := make(chan []string, 1)
	events := make(chan string)
	go watchInput(ctx, inputPoller, sets, events)
	return InputWatch{TTYs: sets, Events: events}
}

// ptmxPath opens a new pty master, not a terminal someone types into.
const ptmxPath = "/dev/pts/ptmx"

// ttyPath reports whether p may be opened to watch for input.
func ttyPath(p string) bool { return banner.IsTTYPath(p) && p != ptmxPath }

// awayWaitDefault is false: typing is measured by ttyReadAt, so AwayMinutes
// does not apply.
const awayWaitDefault = false

// ttyReadAt returns when the agent last read typed input from tty: the
// kernel moves a pts's atime on each read (in 8-second steps); output moves
// only mtime. It only stats the path, never opens it.
func ttyReadAt(tty string) (time.Time, bool) {
	if !ttyPath(tty) {
		return time.Time{}, false
	}
	var st unix.Stat_t
	if err := unix.Stat(tty, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFCHR {
		return time.Time{}, false
	}
	return time.Unix(st.Atim.Unix()), true
}

// PendingInput returns how many typed bytes wait unread on tty, or 0.
func PendingInput(tty string) int {
	if !ttyPath(tty) {
		return 0
	}
	fd, err := inputPoller.open(tty)
	if err != nil {
		return 0
	}
	defer inputPoller.close(fd)
	n, err := inputPoller.unread(fd)
	if err != nil {
		return 0
	}
	return n
}

// watchInput polls the newest set until ctx ends. A tty that was reported,
// hung up or failed leaves the set until a new set comes.
func watchInput(ctx context.Context, p ttyPoller, sets <-chan []string, events chan<- string) {
	var watch []string
	for {
		if len(watch) == 0 {
			select {
			case <-ctx.Done():
				return
			case set, ok := <-sets:
				if !ok {
					return
				}
				watch = ttyPaths(set)
			}
		} else {
			select {
			case <-ctx.Done():
				return
			case set, ok := <-sets:
				if !ok {
					return
				}
				watch = ttyPaths(set)
			default:
			}
		}
		watch = pollInput(ctx, p, watch, events)
	}
}

// ttyPaths returns the terminal paths in set, once each.
func ttyPaths(set []string) []string {
	var out []string
	for _, t := range set {
		if ttyPath(t) && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// pollInput polls watch once, reports ttys with unread input and returns
// the ttys to keep watching.
func pollInput(ctx context.Context, p ttyPoller, watch []string, events chan<- string) []string {
	fds := make([]unix.PollFd, 0, len(watch))
	paths := make([]string, 0, len(watch))
	for _, t := range watch {
		fd, err := p.open(t)
		if err != nil {
			continue // gone: dropped
		}
		fds = append(fds, unix.PollFd{Fd: int32(fd), Events: unix.POLLIN})
		paths = append(paths, t)
	}
	defer func() {
		for _, f := range fds {
			p.close(int(f.Fd))
		}
	}()
	if len(fds) == 0 {
		return nil
	}
	if _, err := p.poll(fds, inputPollMillis); err != nil {
		if !errors.Is(err, unix.EINTR) {
			select { // do not spin on a failing poll
			case <-time.After(inputPollMillis * time.Millisecond):
			case <-ctx.Done():
			}
		}
		return paths
	}
	keep := paths[:0:0]
	for i, f := range fds {
		switch {
		case f.Revents == 0:
			keep = append(keep, paths[i])
		case f.Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0:
			// The terminal closed: nothing to thaw on it.
		case f.Revents&unix.POLLIN != 0:
			// Readable with nothing unread is dropped too, or poll would spin on it.
			if n, err := p.unread(int(f.Fd)); err == nil && n > 0 {
				select {
				case events <- paths[i]:
				case <-ctx.Done():
					return nil
				}
			}
		}
	}
	return keep
}
