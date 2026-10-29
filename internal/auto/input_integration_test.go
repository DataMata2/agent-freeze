//go:build integration && linux

package auto

import (
	"context"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openPTY opens a fresh pty pair of the test's own and returns the master fd and the slave path.
func openPTY(t *testing.T) (int, string) {
	t.Helper()
	master, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { unix.Close(master) })
	if err := unix.IoctlSetPointerInt(master, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlock pty: %v", err)
	}
	n, err := unix.IoctlGetInt(master, unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("pty number: %v", err)
	}
	// Raw mode, as an agent's terminal is: one byte is readable without a newline.
	tio, err := unix.IoctlGetTermios(master, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	tio.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
	tio.Iflag &^= unix.ICRNL | unix.IXON
	tio.Cc[unix.VMIN], tio.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(master, unix.TCSETS, tio); err != nil {
		t.Fatal(err)
	}
	return master, "/dev/pts/" + strconv.Itoa(n)
}

func TestWatchInputSeesTypingWithoutConsumingIt(t *testing.T) {
	master, slavePath := openPTY(t)
	slave, err := sysPoller{}.open(slavePath)
	if err != nil {
		t.Fatalf("open %s: %v", slavePath, err)
	}
	defer unix.Close(slave)
	if n := PendingInput(slavePath); n != 0 {
		t.Fatalf("PendingInput before typing = %d", n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := WatchInput(ctx)
	in.TTYs <- []string{slavePath}
	time.Sleep(100 * time.Millisecond) // let the watcher start polling
	if _, err := unix.Write(master, []byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-in.Events:
		if got != slavePath {
			t.Fatalf("event for %q, want %q", got, slavePath)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no input event")
	}

	if n, err := unix.IoctlGetInt(slave, unix.TIOCINQ); err != nil || n != 1 {
		t.Fatalf("TIOCINQ after the event = %d, %v; want 1 (the watcher must not read)", n, err)
	}
	if n := PendingInput(slavePath); n != 1 {
		t.Fatalf("PendingInput = %d, want 1", n)
	}
	buf := make([]byte, 8)
	n, err := unix.Read(slave, buf)
	if err != nil || string(buf[:n]) != "x" {
		t.Fatalf("read %q, %v; want \"x\"", buf[:n], err)
	}
}
