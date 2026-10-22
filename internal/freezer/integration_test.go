//go:build integration && linux

package freezer

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/DataMata2/agent-freeze/internal/banner"
	"github.com/DataMata2/agent-freeze/internal/discover"
)

// TestHelperProcess is the process the integration test freezes. It only
// does anything when AFZ_HELPER=1. It prints "started", waits for a line on
// stdin (so the test can move it into its own origin cgroup first, making
// the 200 MiB charged there), then allocates and prints "ready".
func TestHelperProcess(t *testing.T) {
	if os.Getenv("AFZ_HELPER") != "1" {
		return
	}
	if _, err := fmt.Println("started"); err != nil {
		os.Exit(1)
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		os.Exit(1)
	}
	buf := make([]byte, 200<<20)
	for i := 0; i < len(buf); i += 4096 {
		buf[i] = 1
	}
	if _, err := fmt.Println("ready"); err != nil {
		os.Exit(1)
	}
	// Safety net: never outlive the test by long, even if orphaned.
	deadline := time.Now().Add(2 * time.Minute)
	n := 0
	for time.Now().Before(deadline) {
		n++
		if _, err := fmt.Println("tick", n); err != nil {
			os.Exit(1) // parent went away
		}
		time.Sleep(100 * time.Millisecond)
	}
	runtime.KeepAlive(buf)
	os.Exit(0)
}

// procCgroup returns the cgroup v2 path of pid ("self" allowed).
func procCgroup(pid string) (string, error) {
	b, err := os.ReadFile("/proc/" + pid + "/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			return p, nil
		}
	}
	return "", fmt.Errorf("no cgroup v2 line in /proc/%s/cgroup", pid)
}

func TestIntegrationFreezeThaw(t *testing.T) {
	f := New(os.Getuid(), os.Getpid())
	m := f.CG
	self, err := procCgroup("self")
	if err != nil {
		t.Skipf("cannot read own cgroup: %v", err)
	}
	if !m.Delegated(self) {
		t.Skipf("own cgroup %q is not under delegated %q", self, m.Delegate)
	}
	if err := syscall.Access(m.Root+m.Delegate+"/app.slice/cgroup.procs", 2 /* W_OK */); err != nil {
		t.Skipf("app.slice/cgroup.procs not writable: %v", err)
	}
	// SAFETY: a per-test-process base, never the production agent-freeze
	// base, a private state file and no session enrichment. List and Status
	// are never called: only the helper's PID is ever touched.
	m.Base = m.Delegate + fmt.Sprintf("/app.slice/agent-freeze-test-%d", os.Getpid())
	f.CG = m
	f.Store.Path = t.TempDir() + "/state.json"
	f.SessionsDir = ""
	f.Banner = true // the helper has no terminal; asserted below
	originCg := m.Path("origin")

	// Spawn the helper, looking like a named claude to /proc.
	name := fmt.Sprintf("afz-integration-%d", os.Getpid())
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "--", "-n", name)
	cmd.Args[0] = "claude"
	cmd.Env = append(os.Environ(), "AFZ_HELPER=1")
	// stdin, stdout and stderr are all pipes, so the helper has no terminal
	// and the banner can never reach a real one.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	pidStr := strconv.Itoa(pid)
	key := fmt.Sprintf("afztest-%d", pid)
	cg := m.Path(key)
	var waitOnce sync.Once
	waitHelper := func() { waitOnce.Do(func() { _ = cmd.Wait() }) }
	// Cleanup runs even on failure/skip: thaw, kill (only our own cmd), wait,
	// remove the key and origin cgroups and the test base. Best effort.
	t.Cleanup(func() {
		if m.Exists(cg) {
			_ = m.SetFrozen(cg, false)
		}
		_ = cmd.Process.Kill()
		waitHelper()
		if stderr.Len() > 0 {
			t.Logf("helper stderr: %s", stderr.String())
		}
		_ = m.RemoveIfEmpty(cg)
		_ = m.RemoveIfEmpty(originCg)
		_ = os.Remove(m.Root + m.Base)
	})

	// Drain stdout fully in the background.
	lines := make(chan string, 1024)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	expect := func(want string) {
		t.Helper()
		select {
		case l, ok := <-lines:
			if !ok || l != want {
				t.Fatalf("helper line = %q (open=%v), want %q", l, ok, want)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("helper did not print %q", want)
		}
	}
	expect("started")
	if got, err := m.Ensure("origin"); err != nil || got != originCg {
		t.Fatalf("Ensure(origin) = %q, %v", got, err)
	}
	if err := m.Move(originCg, pid); err != nil {
		t.Fatalf("Move to origin: %v", err)
	}
	if _, err := stdin.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	expect("ready")
	if now, err := procCgroup(pidStr); err != nil || now != originCg {
		t.Fatalf("helper cgroup = %q, %v; want %q", now, err, originCg)
	}

	// Build the group by hand; never discover it (under a Claude-run test it
	// would sit inside the real Claude group).
	procs, err := f.Proc.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	hp, ok := procs[pid]
	if !ok {
		t.Fatal("helper missing from snapshot")
	}
	if got := discover.Members(procs, pid, os.Getpid()); !slices.Equal(got, []int{pid}) {
		t.Fatalf("helper members = %v, want [%d]; refusing to freeze", got, pid)
	}
	if len(hp.Args) == 0 || hp.Args[0] != "claude" {
		t.Errorf("helper argv = %q, want argv0 claude", hp.Args)
	}
	g := GroupView{
		Group: discover.Group{
			ID: pidStr, Key: key, Agent: "claude", Name: name,
			RootPID: pid, StartTime: hp.StartTime, PIDs: []int{pid}, RSS: hp.RSS,
		},
		State:     "running",
		Delegated: true,
	}
	if tty := banner.TTY(f.Proc, pid); tty != "" {
		t.Fatalf("helper has terminal %q; refusing to write a banner to it", tty)
	}
	t.Logf("helper pid %d argv %q, origin %q, key cgroup %q, VmRSS %d MiB", pid, hp.Args, originCg, cg, hp.RSS>>20)

	// Freeze.
	r := f.Freeze(g, false)
	t.Logf("Freeze: %+v (freed %d MiB)", r, (int64(r.RSSBefore)-int64(r.RSSAfter))>>20)
	if !r.OK {
		t.Fatalf("Freeze failed: %s", r.Error)
	}
	if strings.Contains(r.Warning, "banner:") {
		t.Errorf("Freeze warning: %s", r.Warning)
	}
	if got, err := m.Procs(cg); err != nil || !slices.Equal(got, []int{pid}) {
		t.Fatalf("key cgroup.procs while frozen = %v, %v; want [%d]", got, err, pid)
	}
	if ents, err := f.Store.Load(); err != nil {
		t.Errorf("state load: %v", err)
	} else if o := ents[key].Origins; len(o) != 1 || o[pid] != originCg {
		t.Errorf("state Origins = %v, want {%d: %s}", o, pid, originCg)
	}
	t.Run("Freed", func(t *testing.T) {
		mi, err := f.Proc.MemInfo()
		if err != nil {
			t.Fatal(err)
		}
		if mi.SwapFree < 300<<20 {
			t.Skipf("SwapFree %d MiB < 300 MiB", mi.SwapFree>>20)
		}
		if r.RSSBefore < r.RSSAfter || r.RSSBefore-r.RSSAfter < 100<<20 {
			t.Errorf("freed %d MiB, want >= 100 MiB", (int64(r.RSSBefore)-int64(r.RSSAfter))>>20)
		}
	})
	// Drain lines already in the pipe until it has been quiet for 50 ms, so
	// a tick written just before the freeze can't fail the quiet window.
	drained := 0
	for done := false; !done; {
		select {
		case _, ok := <-lines:
			if !ok {
				t.Fatal("helper output closed while frozen")
			}
			drained++
		case <-time.After(50 * time.Millisecond):
			done = true
		}
	}
	t.Logf("frozen; drained %d pending line(s)", drained)
	select {
	case l, ok := <-lines:
		if !ok {
			t.Fatal("helper output closed while frozen")
		}
		t.Errorf("helper printed %q while frozen", l)
	case <-time.After(300 * time.Millisecond):
	}

	// Thaw.
	g.State = "frozen"
	tr := f.Thaw(g)
	t.Logf("Thaw: %+v", tr)
	if !tr.OK {
		t.Fatalf("Thaw failed: %s", tr.Error)
	}
	if tr.Warning != "" {
		t.Errorf("Thaw warning: %s", tr.Warning)
	}
	select {
	case l, ok := <-lines:
		if !ok {
			t.Fatal("helper output closed after thaw")
		}
		t.Logf("output resumed after thaw: %q", l)
	case <-time.After(time.Second):
		t.Error("no helper output within 1s of thaw")
	}
	if now, err := procCgroup(pidStr); err != nil || now != originCg {
		t.Errorf("helper cgroup after thaw = %q, %v; want %q", now, err, originCg)
	}
	if m.Exists(cg) {
		t.Errorf("key cgroup %s still exists after thaw", cg)
	}
	if ents, err := f.Store.Load(); err != nil || len(ents) != 0 {
		t.Errorf("state after thaw = %v, %v", ents, err)
	}
}
