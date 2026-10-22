//go:build integration && linux

package cgroup

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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

// kbField reads a "Name:   123 kB" field from a /proc file, in bytes.
func kbField(path, name string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, name+":"); ok {
			f := strings.Fields(v)
			if len(f) == 0 {
				break
			}
			n, err := strconv.ParseUint(f[0], 10, 64)
			if err != nil {
				return 0, err
			}
			return n * 1024, nil
		}
	}
	return 0, fmt.Errorf("%s: no %s field", path, name)
}

type reclaimWrite struct {
	path, payload string
	err           error
}

func TestIntegrationFreezeReclaimThaw(t *testing.T) {
	uid := os.Getuid()
	m := New(uid)
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
	// SAFETY: a per-test-process base, never the production agent-freeze base.
	m.Base = m.Delegate + fmt.Sprintf("/app.slice/agent-freeze-test-%d", os.Getpid())
	const key = "helper"
	cg := m.Path(key)
	// The helper's own origin cgroup. cgroup v2 memory charges stay with the
	// cgroup that faulted the pages in, so the helper allocates here and
	// Reclaim(origin) then targets only the helper's memory, not the shared
	// terminal scope this test runs in.
	originCg := m.Path("origin")

	// Record every write to memory.reclaim so a rejected swappiness= is reported.
	var (
		recMu    sync.Mutex
		reclaims []reclaimWrite
	)
	origWrite := writeFile
	writeFile = func(path, s string, appendMode bool) error {
		err := origWrite(path, s, appendMode)
		if strings.HasSuffix(path, "/memory.reclaim") {
			recMu.Lock()
			reclaims = append(reclaims, reclaimWrite{path, s, err})
			recMu.Unlock()
		}
		return err
	}
	t.Cleanup(func() { writeFile = origWrite })

	// Spawn the helper.
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "AFZ_HELPER=1")
	cmd.Stderr = os.Stderr
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
	var waitOnce sync.Once
	waitHelper := func() { waitOnce.Do(func() { _ = cmd.Wait() }) }
	// Cleanup runs even on failure/skip: thaw, kill (only our own cmd), wait,
	// remove the key cgroup, remove the test base. Best effort, errors ignored.
	t.Cleanup(func() {
		if m.Exists(cg) {
			_ = m.SetFrozen(cg, false)
		}
		_ = cmd.Process.Kill()
		waitHelper()
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
	spawnCg, err := procCgroup(pidStr)
	if err != nil {
		t.Fatal(err)
	}
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
	origin, err := procCgroup(pidStr)
	if err != nil {
		t.Fatal(err)
	}
	if origin != originCg {
		t.Fatalf("helper origin cgroup = %q, want %q", origin, originCg)
	}
	t.Logf("helper pid %d, spawned in %q, origin cgroup %q, test cgroup %q", pid, spawnCg, origin, cg)

	// Ensure + Move.
	if got, err := m.Ensure(key); err != nil || got != cg {
		t.Fatalf("Ensure = %q, %v", got, err)
	}
	if err := m.Move(cg, pid); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if now, err := procCgroup(pidStr); err != nil || now != cg {
		t.Fatalf("helper cgroup after Move = %q, %v; want %q", now, err, cg)
	}
	if pids, err := m.Procs(cg); err != nil || len(pids) != 1 || pids[0] != pid {
		t.Fatalf("Procs = %v, %v; want [%d]", pids, err, pid)
	}

	// Freeze.
	if err := m.SetFrozen(cg, true); err != nil {
		t.Fatalf("SetFrozen(true): %v", err)
	}
	if err := m.WaitFrozen(cg, true, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// Drain lines already in the pipe until it has been quiet for 50 ms, so
	// a tick written just before the freeze can't fail the quiet window.
	drained := 0
	for done := false; !done; {
		select {
		case <-lines:
			drained++
		case <-time.After(50 * time.Millisecond):
			done = true
		}
	}
	t.Logf("frozen; drained %d pending line(s)", drained)
	select {
	case l := <-lines:
		t.Errorf("helper printed %q while frozen", l)
	case <-time.After(300 * time.Millisecond):
	}

	// Reclaim.
	statusPath := "/proc/" + pidStr + "/status"
	rss0, err := kbField(statusPath, "VmRSS")
	if err != nil {
		t.Fatal(err)
	}
	swap0, err := kbField(statusPath, "VmSwap")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Reclaim(origin, 200<<20); err != nil {
		t.Errorf("Reclaim(origin): %v", err)
	}
	if err := m.Reclaim(cg, 200<<20); err != nil {
		t.Errorf("Reclaim(test cgroup): %v", err)
	}
	rss1, rssErr := kbField(statusPath, "VmRSS")
	if rssErr != nil {
		t.Errorf("read VmRSS after reclaim: %v", rssErr)
	}
	swap1, swapErr := kbField(statusPath, "VmSwap")
	if swapErr != nil {
		t.Errorf("read VmSwap after reclaim: %v", swapErr)
	}
	t.Logf("helper VmRSS %d MiB -> %d MiB, VmSwap %d MiB -> %d MiB", rss0>>20, rss1>>20, swap0>>20, swap1>>20)
	recMu.Lock()
	for _, r := range reclaims {
		t.Logf("memory.reclaim write %s %q -> err=%v", r.path, r.payload, r.err)
		if strings.HasSuffix(r.payload, " swappiness=200") && errors.Is(r.err, syscall.EINVAL) {
			t.Errorf("kernel rejected swappiness= in memory.reclaim (%s): %v", r.path, r.err)
		}
	}
	recMu.Unlock()
	t.Run("ReclaimSize", func(t *testing.T) {
		if rssErr != nil || swapErr != nil {
			t.Skip("VmRSS/VmSwap after reclaim unreadable (reported above)")
		}
		swapFree, err := kbField("/proc/meminfo", "SwapFree")
		if err != nil {
			t.Fatal(err)
		}
		if swapFree < 300<<20 {
			t.Skipf("SwapFree %d MiB < 300 MiB", swapFree>>20)
		}
		if rss0 < rss1 || rss0-rss1 < 100<<20 {
			t.Errorf("VmRSS dropped by %d MiB, want >= 100 MiB", (int64(rss0)-int64(rss1))>>20)
		}
		if swap1 <= swap0 {
			t.Errorf("VmSwap did not rise (%d -> %d)", swap0, swap1)
		}
	})

	// Thaw.
	if err := m.SetFrozen(cg, false); err != nil {
		t.Fatalf("SetFrozen(false): %v", err)
	}
	if err := m.WaitFrozen(cg, false, 5*time.Second); err != nil {
		t.Fatal(err)
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

	// Move back to origin, kill, remove.
	if err := m.Move(origin, pid); err != nil {
		t.Fatalf("Move back to origin: %v", err)
	}
	if now, err := procCgroup(pidStr); err != nil || now != origin {
		t.Errorf("helper cgroup after move back = %q, %v; want %q", now, err, origin)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Errorf("kill helper: %v", err)
	}
	waitHelper()
	for _, c := range []string{cg, originCg} {
		if err := m.RemoveIfEmpty(c); err != nil {
			t.Errorf("RemoveIfEmpty(%s): %v", c, err)
		}
		if m.Exists(c) {
			t.Errorf("test cgroup %s still exists", c)
		}
	}
	if keys, err := m.Keys(); err != nil || len(keys) != 0 {
		t.Errorf("Keys after removal = %v, %v", keys, err)
	}
}
