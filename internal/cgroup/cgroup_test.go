//go:build linux

package cgroup

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const testDelegate = "/user.slice/user-1000.slice/user@1000.service"

func fakeManager(t *testing.T) Manager {
	t.Helper()
	return Manager{
		Root:     t.TempDir(),
		Delegate: testDelegate,
		Base:     testDelegate + "/app.slice/agent-freeze",
	}
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func mustWrite(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// swapWriteFile replaces the package writer for one test and restores it.
func swapWriteFile(t *testing.T, f func(path, s string, appendMode bool) error) {
	t.Helper()
	orig := writeFile
	writeFile = f
	t.Cleanup(func() { writeFile = orig })
}

func TestNew(t *testing.T) {
	m := New(1000)
	if m.Root != "/sys/fs/cgroup" {
		t.Errorf("Root = %q", m.Root)
	}
	if m.Delegate != testDelegate {
		t.Errorf("Delegate = %q", m.Delegate)
	}
	if m.Base != testDelegate+"/app.slice/agent-freeze" {
		t.Errorf("Base = %q", m.Base)
	}
	if got := m.Path("k"); got != m.Base+"/k" {
		t.Errorf("Path = %q", got)
	}
}

func TestDelegated(t *testing.T) {
	m := New(1000)
	cases := map[string]bool{
		testDelegate:                  true,
		testDelegate + "/app.slice/a": true,
		testDelegate + "X/a":          false,
		"/system.slice/x":             false,
		"/user.slice/user-1000.slice": false,
	}
	for cg, want := range cases {
		if got := m.Delegated(cg); got != want {
			t.Errorf("Delegated(%q) = %v, want %v", cg, got, want)
		}
	}
}

func TestEnsureCreatesAndEnablesMemory(t *testing.T) {
	m := fakeManager(t)
	got, err := m.Ensure("claude-1-2")
	if err != nil {
		t.Fatal(err)
	}
	if got != m.Path("claude-1-2") {
		t.Errorf("Ensure returned %q, want %q", got, m.Path("claude-1-2"))
	}
	if fi, err := os.Stat(m.Root + got); err != nil || !fi.IsDir() {
		t.Fatalf("cgroup dir missing: %v", err)
	}
	if !m.Exists(got) {
		t.Error("Exists = false after Ensure")
	}
	if s := readString(t, m.Root+m.Base+"/cgroup.subtree_control"); s != "+memory" {
		t.Errorf("subtree_control = %q", s)
	}
	if _, err := m.Ensure("claude-1-2"); err != nil {
		t.Errorf("second Ensure: %v", err)
	}
	for _, bad := range []string{"../x", "a/b", ".", "..", ""} {
		if _, err := m.Ensure(bad); err == nil {
			t.Errorf("Ensure(%q) succeeded, want error", bad)
		}
	}
}

func TestExistsMissing(t *testing.T) {
	m := fakeManager(t)
	if m.Exists(m.Path("nope")) {
		t.Error("Exists = true for missing cgroup")
	}
}

func TestMoveAppends(t *testing.T) {
	m := fakeManager(t)
	cg, err := m.Ensure("k")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Move(cg, 5); err != nil {
		t.Fatal(err)
	}
	if err := m.Move(cg, 6); err != nil {
		t.Fatal(err)
	}
	if s := readString(t, m.Root+cg+"/cgroup.procs"); s != "5\n6\n" {
		t.Errorf("cgroup.procs = %q", s)
	}
	pids, err := m.Procs(cg)
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 2 || pids[0] != 5 || pids[1] != 6 {
		t.Errorf("Procs = %v", pids)
	}
}

func TestMoveGone(t *testing.T) {
	m := fakeManager(t)
	swapWriteFile(t, func(path, s string, appendMode bool) error {
		return &os.PathError{Op: "write", Path: path, Err: syscall.ESRCH}
	})
	err := m.Move(m.Path("k"), 42)
	if !errors.Is(err, ErrGone) {
		t.Errorf("Move error = %v, want ErrGone", err)
	}
}

func TestMoveOtherError(t *testing.T) {
	m := fakeManager(t)
	swapWriteFile(t, func(path, s string, appendMode bool) error {
		return &os.PathError{Op: "write", Path: path, Err: syscall.EACCES}
	})
	err := m.Move(m.Path("k"), 42)
	if err == nil || errors.Is(err, ErrGone) {
		t.Errorf("Move error = %v, want non-ErrGone error", err)
	}
}

func TestProcsEmptyAndMissing(t *testing.T) {
	m := fakeManager(t)
	cg, _ := m.Ensure("k")
	mustWrite(t, m.Root+cg+"/cgroup.procs", "")
	pids, err := m.Procs(cg)
	if err != nil || len(pids) != 0 {
		t.Errorf("Procs = %v, %v", pids, err)
	}
	if _, err := m.Procs(m.Path("missing")); err == nil {
		t.Error("Procs on missing cgroup: want error")
	}
}

func TestSetFrozenAndFrozen(t *testing.T) {
	m := fakeManager(t)
	cg, _ := m.Ensure("k")
	if err := m.SetFrozen(cg, true); err != nil {
		t.Fatal(err)
	}
	if s := readString(t, m.Root+cg+"/cgroup.freeze"); s != "1" {
		t.Errorf("cgroup.freeze = %q", s)
	}
	if err := m.SetFrozen(cg, false); err != nil {
		t.Fatal(err)
	}
	if s := readString(t, m.Root+cg+"/cgroup.freeze"); s != "0" {
		t.Errorf("cgroup.freeze = %q", s)
	}
	mustWrite(t, m.Root+cg+"/cgroup.events", "populated 1\nfrozen 1\n")
	if f, err := m.Frozen(cg); err != nil || !f {
		t.Errorf("Frozen = %v, %v; want true", f, err)
	}
	mustWrite(t, m.Root+cg+"/cgroup.events", "populated 1\nfrozen 0\n")
	if f, err := m.Frozen(cg); err != nil || f {
		t.Errorf("Frozen = %v, %v; want false", f, err)
	}
}

func TestWaitFrozen(t *testing.T) {
	m := fakeManager(t)
	cg, _ := m.Ensure("k")
	mustWrite(t, m.Root+cg+"/cgroup.events", "populated 1\nfrozen 1\n")
	if err := m.WaitFrozen(cg, true, time.Second); err != nil {
		t.Errorf("WaitFrozen: %v", err)
	}
}

func TestWaitFrozenTimeout(t *testing.T) {
	m := fakeManager(t)
	cg, _ := m.Ensure("k")
	mustWrite(t, m.Root+cg+"/cgroup.events", "populated 1\nfrozen 0\n")
	start := time.Now()
	err := m.WaitFrozen(cg, true, 60*time.Millisecond)
	if err == nil {
		t.Fatal("WaitFrozen: want timeout error")
	}
	if el := time.Since(start); el < 60*time.Millisecond || el > 2*time.Second {
		t.Errorf("WaitFrozen took %s", el)
	}
}

func TestReclaimWritesSwappiness(t *testing.T) {
	m := fakeManager(t)
	cg, _ := m.Ensure("k")
	if err := m.Reclaim(cg, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Root + cg + "/memory.reclaim"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Reclaim(0) created memory.reclaim: %v", err)
	}
	if err := m.Reclaim(cg, 1048576); err != nil {
		t.Fatal(err)
	}
	if s := readString(t, m.Root+cg+"/memory.reclaim"); s != "1048576 swappiness=200" {
		t.Errorf("memory.reclaim = %q", s)
	}
}

func TestReclaimEINVALFallsBack(t *testing.T) {
	m := fakeManager(t)
	var payloads []string
	swapWriteFile(t, func(path, s string, appendMode bool) error {
		payloads = append(payloads, s)
		if len(payloads) == 1 {
			return &os.PathError{Op: "write", Path: path, Err: syscall.EINVAL}
		}
		return nil
	})
	if err := m.Reclaim(m.Path("k"), 1048576); err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if len(payloads) != 2 || payloads[0] != "1048576 swappiness=200" || payloads[1] != "1048576" {
		t.Errorf("payloads = %q", payloads)
	}
}

func TestReclaimEAGAINIsNil(t *testing.T) {
	m := fakeManager(t)
	calls := 0
	swapWriteFile(t, func(path, s string, appendMode bool) error {
		calls++
		return &os.PathError{Op: "write", Path: path, Err: syscall.EAGAIN}
	})
	if err := m.Reclaim(m.Path("k"), 1048576); err != nil {
		t.Errorf("Reclaim with EAGAIN: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestReclaimEINVALThenEAGAINIsNil(t *testing.T) {
	m := fakeManager(t)
	calls := 0
	swapWriteFile(t, func(path, s string, appendMode bool) error {
		calls++
		if calls == 1 {
			return &os.PathError{Op: "write", Path: path, Err: syscall.EINVAL}
		}
		return &os.PathError{Op: "write", Path: path, Err: syscall.EAGAIN}
	})
	if err := m.Reclaim(m.Path("k"), 1048576); err != nil {
		t.Errorf("Reclaim: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestReclaimOtherErrorReturned(t *testing.T) {
	m := fakeManager(t)
	swapWriteFile(t, func(path, s string, appendMode bool) error {
		return &os.PathError{Op: "write", Path: path, Err: syscall.EACCES}
	})
	if err := m.Reclaim(m.Path("k"), 1048576); !errors.Is(err, syscall.EACCES) {
		t.Errorf("Reclaim error = %v, want EACCES", err)
	}
}

func TestKeysMissingBase(t *testing.T) {
	m := fakeManager(t)
	keys, err := m.Keys()
	if keys != nil || err != nil {
		t.Errorf("Keys = %v, %v; want nil, nil", keys, err)
	}
}

func TestKeys(t *testing.T) {
	m := fakeManager(t)
	for _, k := range []string{"b", "a"} {
		if _, err := m.Ensure(k); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := m.Keys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Errorf("Keys = %v (files like cgroup.subtree_control must be excluded)", keys)
	}
}

func TestRemoveIfEmpty(t *testing.T) {
	m := fakeManager(t)
	cg, _ := m.Ensure("k")
	mustWrite(t, m.Root+cg+"/cgroup.procs", "7\n")
	mustWrite(t, m.Root+cg+"/cgroup.freeze", "0")
	if err := m.RemoveIfEmpty(cg); err != nil {
		t.Fatal(err)
	}
	if !m.Exists(cg) {
		t.Fatal("non-empty cgroup was removed")
	}
	mustWrite(t, m.Root+cg+"/cgroup.procs", "")
	if err := m.RemoveIfEmpty(cg); err != nil {
		t.Fatal(err)
	}
	if m.Exists(cg) {
		t.Fatal("empty cgroup still exists")
	}
	if err := m.RemoveIfEmpty(cg); err != nil {
		t.Errorf("second RemoveIfEmpty: %v", err)
	}
}

func TestSetFrozenTruncates(t *testing.T) {
	m := fakeManager(t)
	cg, _ := m.Ensure("k")
	mustWrite(t, m.Root+cg+"/cgroup.freeze", "xx")
	if err := m.SetFrozen(cg, true); err != nil {
		t.Fatal(err)
	}
	if s := readString(t, m.Root+cg+"/cgroup.freeze"); s != "1" {
		t.Errorf("cgroup.freeze = %q, want %q", s, "1")
	}
}

func TestReclaimTruncates(t *testing.T) {
	m := fakeManager(t)
	cg, _ := m.Ensure("k")
	mustWrite(t, m.Root+cg+"/memory.reclaim", "junk junk junk junk junk junk junk junk")
	if err := m.Reclaim(cg, 1048576); err != nil {
		t.Fatal(err)
	}
	if s := readString(t, m.Root+cg+"/memory.reclaim"); s != "1048576 swappiness=200" {
		t.Errorf("memory.reclaim = %q", s)
	}
}
