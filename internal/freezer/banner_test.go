//go:build linux

package freezer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DataMata2/agent-freeze/internal/procfs"
	"github.com/DataMata2/agent-freeze/internal/procfs/procfstest"
)

// bannerCalls replaces the banner and SIGWINCH hooks with recorders for the
// rest of the test and returns the recorded calls. Unit tests never write
// to a real terminal or signal a real process.
func bannerCalls(t *testing.T, showErr, clearErr error) *[]string {
	t.Helper()
	var calls []string
	oldTTY, oldShow, oldClear, oldWinch := ttyOf, showBanner, clearBanner, sendWinch
	// The fake /proc's fd links name /dev/pts paths; resolve them by
	// readlink only, so no real /dev path is ever stat'ed.
	ttyOf = func(proc procfs.FS, pid int) string {
		s, _ := os.Readlink(filepath.Join(proc.Root, strconv.Itoa(pid), "fd", "0"))
		return s
	}
	showBanner = func(tty, name string, at time.Time) error {
		calls = append(calls, fmt.Sprintf("show %s %s %s", tty, name, at.UTC().Format(time.RFC3339)))
		return showErr
	}
	clearBanner = func(tty, name string) error {
		calls = append(calls, fmt.Sprintf("clear %s %s", tty, name))
		return clearErr
	}
	sendWinch = func(pid int) error {
		calls = append(calls, fmt.Sprintf("winch %d", pid))
		return nil
	}
	t.Cleanup(func() { ttyOf, showBanner, clearBanner, sendWinch = oldTTY, oldShow, oldClear, oldWinch })
	return &calls
}

// linkTTY makes the fake /proc/<pid>/fd/0 point at tty.
func linkTTY(t *testing.T, proc string, pid int, tty string) {
	t.Helper()
	dir := filepath.Join(proc, strconv.Itoa(pid), "fd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tty, filepath.Join(dir, "0")); err != nil {
		t.Fatal(err)
	}
}

var showAt = "show /dev/pts/7 %s " + fixedNow.Format(time.RFC3339)

// freezeThaw102 freezes and thaws the base fixture's group 102 with the
// kernel simulated, returning both results.
func freezeThaw102(t *testing.T, e *env, beforeThaw func(g *GroupView)) (Result, Result) {
	t.Helper()
	kd := e.keyDir(key102)
	put(t, kd+"/cgroup.events", "frozen 1\n")
	e.simulateKernel(t, nil)
	g := e.group(t, 102)
	fr := e.f.Freeze(g, true)
	if !fr.OK {
		t.Fatalf("Freeze = %+v", fr)
	}
	put(t, kd+"/cgroup.events", "frozen 0\n")
	g = e.group(t, 102)
	if beforeThaw != nil {
		beforeThaw(&g)
	}
	return fr, e.f.Thaw(g)
}

func TestBannerFreezeThaw(t *testing.T) {
	e := newEnv(t)
	e.f.Banner = true
	linkTTY(t, e.proc, 102, "/dev/pts/7")
	calls := bannerCalls(t, nil, nil)
	name := e.group(t, 102).Name

	fr, tr := freezeThaw102(t, e, nil)
	if fr.Warning != "" || !tr.OK || tr.Warning != "" {
		t.Fatalf("Freeze = %+v, Thaw = %+v", fr, tr)
	}
	want := []string{fmt.Sprintf(showAt, name), "clear /dev/pts/7 " + name, "winch 102"}
	if !slices.Equal(*calls, want) {
		t.Errorf("calls = %q, want %q", *calls, want)
	}
}

func TestBannerDisabled(t *testing.T) {
	e := newEnv(t)
	linkTTY(t, e.proc, 102, "/dev/pts/7")
	calls := bannerCalls(t, nil, nil)

	fr, tr := freezeThaw102(t, e, nil)
	if fr.Warning != "" || !tr.OK || tr.Warning != "" {
		t.Fatalf("Freeze = %+v, Thaw = %+v", fr, tr)
	}
	if len(*calls) != 0 {
		t.Errorf("calls = %q, want none", *calls)
	}
}

func TestBannerNoTTY(t *testing.T) {
	e := newEnv(t)
	e.f.Banner = true
	calls := bannerCalls(t, nil, nil)

	fr, tr := freezeThaw102(t, e, nil)
	if fr.Warning != "" || !tr.OK || tr.Warning != "" {
		t.Fatalf("Freeze = %+v, Thaw = %+v", fr, tr)
	}
	// No terminal: nothing to show, clear or redraw.
	if len(*calls) != 0 {
		t.Errorf("calls = %q, want none", *calls)
	}
}

func TestBannerErrorsAreWarnings(t *testing.T) {
	e := newEnv(t)
	e.f.Banner = true
	linkTTY(t, e.proc, 102, "/dev/pts/7")
	bannerCalls(t, errors.New("show boom"), errors.New("clear boom"))

	fr, tr := freezeThaw102(t, e, nil)
	if !fr.OK || fr.Error != "" || fr.Warning != "banner: show boom" {
		t.Errorf("Freeze = %+v", fr)
	}
	if !tr.OK || tr.Error != "" || tr.Warning != "banner: clear boom" {
		t.Errorf("Thaw = %+v", tr)
	}
}

func TestBannerWarningJoinsExisting(t *testing.T) {
	e := newEnv(t)
	e.f.Banner = true
	linkTTY(t, e.proc, 102, "/dev/pts/7")
	bannerCalls(t, errors.New("show boom"), nil)
	put(t, e.keyDir(key102)+"/cgroup.events", "frozen 1\n")
	// memory.reclaim as a directory makes the reclaim write fail.
	if err := os.MkdirAll(e.dir(originCg)+"/memory.reclaim", 0o755); err != nil {
		t.Fatal(err)
	}
	r := e.f.Freeze(e.group(t, 102), true)
	if !r.OK || !strings.HasPrefix(r.Warning, "reclaim: ") || !strings.HasSuffix(r.Warning, "; banner: show boom") {
		t.Errorf("Freeze = %+v", r)
	}
}

func TestBannerNotOnFailedOrAlreadyFrozen(t *testing.T) {
	e := newEnv(t)
	e.f.Banner = true
	linkTTY(t, e.proc, 102, "/dev/pts/7")
	calls := bannerCalls(t, nil, nil)
	g := e.group(t, 102)
	put(t, e.keyDir(key102)+"/cgroup.events", "frozen 0\n") // never freezes: timeout, rollback
	if r := e.f.Freeze(g, true); r.OK {
		t.Fatalf("Freeze = %+v, want failure", r)
	}
	g.State = "frozen"
	if r := e.f.Freeze(g, true); !r.OK || r.Warning != "already frozen" {
		t.Fatalf("Freeze = %+v", r)
	}
	if len(*calls) != 0 {
		t.Errorf("calls = %q, want none", *calls)
	}
}

func TestBannerNoWinchWhenStartTimeChanged(t *testing.T) {
	e := newEnv(t)
	e.f.Banner = true
	linkTTY(t, e.proc, 102, "/dev/pts/7")
	calls := bannerCalls(t, nil, nil)
	inner := clearBanner
	// The process exits and its PID is reused between the clear and the
	// signal.
	clearBanner = func(tty, name string) error {
		p := baseProcs()[1]
		p.StartTime = 9999
		procfstest.Write(t, e.proc, p)
		return inner(tty, name)
	}

	_, tr := freezeThaw102(t, e, nil)
	if !tr.OK || tr.Warning != "" {
		t.Fatalf("Thaw = %+v", tr)
	}
	name := e.group(t, 102).Name
	if want := []string{fmt.Sprintf(showAt, name), "clear /dev/pts/7 " + name}; !slices.Equal(*calls, want) {
		t.Errorf("calls = %q, want %q", *calls, want)
	}
}

func TestBannerThawRootReplaced(t *testing.T) {
	e := newEnv(t)
	e.f.Banner = true
	linkTTY(t, e.proc, 102, "/dev/pts/7")
	calls := bannerCalls(t, nil, nil)

	_, tr := freezeThaw102(t, e, func(g *GroupView) { g.StartTime = 1234 })
	if !tr.OK || tr.Warning != "" {
		t.Fatalf("Thaw = %+v", tr)
	}
	// Only the freeze's show: PID 102 is not the group's root any more.
	if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "show ") {
		t.Errorf("calls = %q, want only the show", *calls)
	}
}

// ptyHostProcs is a claude pty host (201) with a node child (202) and two
// claude children (203, 205).
func ptyHostProcs() []procfstest.Fake {
	host := []string{"claude", "bg-pty-host", "--bg-pty-host", "/tmp/s.sock", "80", "24", "--", "claude", "-n", "work"}
	return []procfstest.Fake{
		{PID: 201, PPID: 1, StartTime: 6000, Comm: "claude", Args: host, Cgroup: originCg, RSSKB: 1000},
		{PID: 202, PPID: 201, StartTime: 6001, Comm: "node", Args: []string{"node", "x.js"}, Cgroup: originCg, RSSKB: 1000},
		{PID: 203, PPID: 201, StartTime: 6002, Comm: "claude", Args: []string{"claude", "-n", "work"}, Cgroup: originCg, RSSKB: 1000},
		{PID: 205, PPID: 201, StartTime: 6003, Comm: "claude", Args: []string{"claude"}, Cgroup: originCg, RSSKB: 1000},
	}
}

func TestBannerPtyHostUsesClaudeChild(t *testing.T) {
	e := newEnv(t, ptyHostProcs()...)
	e.f.Banner = true
	linkTTY(t, e.proc, 201, "/dev/pts/1")
	linkTTY(t, e.proc, 202, "/dev/pts/2")
	linkTTY(t, e.proc, 203, "/dev/pts/3")
	linkTTY(t, e.proc, 205, "/dev/pts/5")
	calls := bannerCalls(t, nil, nil)
	e.simulateKernel(t, nil)
	g := e.group(t, 201)
	kd := e.keyDir(g.Key)
	put(t, kd+"/cgroup.events", "frozen 1\n")

	if r := e.f.Freeze(g, false); !r.OK || r.Warning != "" {
		t.Fatalf("Freeze = %+v", r)
	}
	put(t, kd+"/cgroup.events", "frozen 0\n")
	g = e.group(t, 201)
	if r := e.f.Thaw(g); !r.OK || r.Warning != "" {
		t.Fatalf("Thaw = %+v", r)
	}
	want := []string{
		"show /dev/pts/3 " + g.Name + " " + fixedNow.Format(time.RFC3339),
		"clear /dev/pts/3 " + g.Name,
		"winch 203",
	}
	if !slices.Equal(*calls, want) {
		t.Errorf("calls = %q, want %q", *calls, want)
	}
}

func TestNewBannerFromEnv(t *testing.T) {
	t.Setenv("AFZ_NO_BANNER", "")
	if f := New(1000, 42); !f.Banner {
		t.Error("Banner = false without AFZ_NO_BANNER")
	}
	t.Setenv("AFZ_NO_BANNER", "1")
	if f := New(1000, 42); f.Banner {
		t.Error("Banner = true with AFZ_NO_BANNER=1")
	}
}

func TestListTTY(t *testing.T) {
	bannerCalls(t, nil, nil)
	t.Run("plain root", func(t *testing.T) {
		e := newEnv(t)
		linkTTY(t, e.proc, 102, "/dev/pts/4")
		if got := e.group(t, 102).TTY; got != "/dev/pts/4" {
			t.Errorf("TTY = %q, want /dev/pts/4", got)
		}
	})
	t.Run("pty host uses lowest claude child", func(t *testing.T) {
		e := newEnv(t, ptyHostProcs()...)
		linkTTY(t, e.proc, 201, "/dev/pts/1")
		linkTTY(t, e.proc, 202, "/dev/pts/2")
		linkTTY(t, e.proc, 203, "/dev/pts/3")
		linkTTY(t, e.proc, 205, "/dev/pts/5")
		if got := e.group(t, 201).TTY; got != "/dev/pts/3" {
			t.Errorf("TTY = %q, want /dev/pts/3", got)
		}
	})
	t.Run("no tty", func(t *testing.T) {
		e := newEnv(t)
		if got := e.group(t, 102).TTY; got != "" {
			t.Errorf("TTY = %q, want empty", got)
		}
	})
}
