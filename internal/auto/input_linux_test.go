package auto

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// fakePoller stands in for the kernel: each path gets fixed revents and a fixed count of unread bytes.
type fakePoller struct {
	mu      sync.Mutex
	revents map[string]int16
	inq     map[string]int
	fds     map[int]string
	next    int
	opened  []string
	polls   chan []string // the paths of each poll, in fd order
}

func newFakePoller() *fakePoller {
	return &fakePoller{revents: map[string]int16{}, inq: map[string]int{}, fds: map[int]string{},
		next: 100, polls: make(chan []string, 1000)}
}

func (p *fakePoller) set(path string, revents int16, inq int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.revents[path], p.inq[path] = revents, inq
}

func (p *fakePoller) open(path string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opened = append(p.opened, path)
	if path == "/dev/pts/missing" {
		return -1, errors.New("no such device")
	}
	p.next++
	p.fds[p.next] = path
	return p.next, nil
}

func (p *fakePoller) poll(fds []unix.PollFd, _ int) (int, error) {
	time.Sleep(time.Millisecond) // a real poll waits
	p.mu.Lock()
	defer p.mu.Unlock()
	paths := make([]string, len(fds))
	n := 0
	for i := range fds {
		paths[i] = p.fds[int(fds[i].Fd)]
		fds[i].Revents = p.revents[paths[i]]
		if fds[i].Revents != 0 {
			n++
		}
	}
	p.mu.Unlock()
	select { // never block the watcher on a full record
	case p.polls <- paths:
	default:
	}
	p.mu.Lock()
	return n, nil
}

func (p *fakePoller) unread(fd int) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inq[p.fds[fd]], nil
}

func (p *fakePoller) close(fd int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.fds[fd]; !ok {
		panic("closed an fd that is not open")
	}
	delete(p.fds, fd)
}

func (p *fakePoller) openPaths() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.opened)
}

func startWatcher(t *testing.T, p *fakePoller) (chan []string, <-chan string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sets := make(chan []string, 1)
	events := make(chan string)
	done := make(chan struct{})
	go func() { watchInput(ctx, p, sets, events); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.fds) != 0 {
			t.Errorf("fds left open: %v", p.fds)
		}
	})
	return sets, events
}

func expectEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("event %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no event for %s", want)
	}
}

func expectNoEvent(t *testing.T, events <-chan string) {
	t.Helper()
	select {
	case got := <-events:
		t.Fatalf("unexpected event %q", got)
	case <-time.After(50 * time.Millisecond):
	}
}

// waitPoll waits for a poll of exactly want.
func waitPoll(t *testing.T, p *fakePoller, want []string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case got := <-p.polls:
			if slices.Equal(got, want) {
				return
			}
		case <-deadline:
			t.Fatalf("no poll of %v", want)
		}
	}
}

func TestWatchInputReportsUnreadInputOnce(t *testing.T) {
	p := newFakePoller()
	p.set("/dev/pts/1", unix.POLLIN, 1)
	sets, events := startWatcher(t, p)
	sets <- []string{"/dev/pts/1", "/dev/pts/2"}
	expectEvent(t, events, "/dev/pts/1")
	// The bytes stay unread, but the tty is out of the set until a new one comes.
	waitPoll(t, p, []string{"/dev/pts/2"})
	expectNoEvent(t, events)
	sets <- []string{"/dev/pts/1"}
	expectEvent(t, events, "/dev/pts/1")
}

func TestWatchInputIgnoresPollInWithNothingUnread(t *testing.T) {
	p := newFakePoller()
	p.set("/dev/pts/1", unix.POLLIN, 0)
	sets, events := startWatcher(t, p)
	sets <- []string{"/dev/pts/1", "/dev/pts/2"}
	// Dropped, or poll would return at once on it forever.
	waitPoll(t, p, []string{"/dev/pts/2"})
	expectNoEvent(t, events)
}

func TestWatchInputEndsWhenTheSetChannelCloses(t *testing.T) {
	p := newFakePoller()
	done := make(chan struct{})
	sets := make(chan []string, 1)
	sets <- []string{"/dev/pts/1"}
	close(sets)
	go func() { watchInput(context.Background(), p, sets, make(chan string)); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchInput kept running after its set channel closed")
	}
}

func TestSysPollerRefusesWhatIsNotATerminal(t *testing.T) {
	for _, path := range []string{"/dev/pts/ptmx", "/dev/null", "/dev/tty-missing", "/etc/passwd"} {
		if fd, err := (sysPoller{}).open(path); err == nil {
			unix.Close(fd)
			t.Errorf("opened %s", path)
		}
	}
}

func TestWatchInputDropsHungUpTTYs(t *testing.T) {
	for _, rev := range []int16{unix.POLLHUP, unix.POLLERR, unix.POLLNVAL, unix.POLLIN | unix.POLLHUP} {
		p := newFakePoller()
		p.set("/dev/pts/1", rev, 1)
		sets, events := startWatcher(t, p)
		sets <- []string{"/dev/pts/1", "/dev/pts/2"}
		waitPoll(t, p, []string{"/dev/pts/2"})
		expectNoEvent(t, events)
	}
}

func TestWatchInputTakesTheNewestSet(t *testing.T) {
	p := newFakePoller()
	sets, events := startWatcher(t, p)
	sets <- []string{"/dev/pts/1"}
	waitPoll(t, p, []string{"/dev/pts/1"})
	sets <- []string{"/dev/pts/2", "/dev/pts/2"}
	waitPoll(t, p, []string{"/dev/pts/2"})
	for range 5 {
		if got := <-p.polls; !slices.Equal(got, []string{"/dev/pts/2"}) {
			t.Fatalf("polled %v after the set changed to [/dev/pts/2]", got)
		}
	}
	sets <- nil
	time.Sleep(20 * time.Millisecond)
	for len(p.polls) > 0 {
		<-p.polls
	}
	select {
	case got := <-p.polls:
		t.Fatalf("polled %v with an empty set", got)
	case <-time.After(50 * time.Millisecond):
	}
	expectNoEvent(t, events)
}

func TestWatchInputOpensOnlyTerminalPaths(t *testing.T) {
	p := newFakePoller()
	p.set("/dev/pts/3", unix.POLLIN, 2)
	sets, events := startWatcher(t, p)
	sets <- []string{"/etc/passwd", "/dev/tty", "/dev/pts/", "/dev/pts/../sda", "", "/dev/frozen", "/dev/pts/ptmx",
		"/dev/pts/missing", "/dev/pts/3"}
	expectEvent(t, events, "/dev/pts/3")
	if got := p.openPaths(); !slices.Equal(got, []string{"/dev/pts/missing", "/dev/pts/3"}) {
		t.Fatalf("opened %v", got)
	}
}

func TestTTYReadAtStatsOnlyTerminalPaths(t *testing.T) {
	for _, p := range []string{"", "/etc/passwd", "/dev/pts/ptmx", "/dev/pts/../null", "/dev/null"} {
		if at, ok := ttyReadAt(p); ok {
			t.Errorf("ttyReadAt(%q) = %v, true; want false", p, at)
		}
	}
}
