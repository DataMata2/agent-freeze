//go:build linux

// Package cgroup manages the cgroup v2 directories agent-freeze uses to
// freeze process groups and push their memory to swap. All cg arguments are
// cgroup paths relative to Root, starting with "/".
package cgroup

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrGone is returned by Move when the process no longer exists.
var ErrGone = errors.New("process exited")

// Manager locates agent-freeze's cgroups inside the user's delegated subtree.
type Manager struct {
	Root     string // "/sys/fs/cgroup"
	Delegate string // "/user.slice/user-<uid>.slice/user@<uid>.service"
	Base     string // Delegate + "/app.slice/agent-freeze"
}

// New returns the Manager for the given user's systemd delegated subtree.
func New(uid int) Manager {
	d := fmt.Sprintf("/user.slice/user-%d.slice/user@%d.service", uid, uid)
	return Manager{Root: "/sys/fs/cgroup", Delegate: d, Base: d + "/app.slice/agent-freeze"}
}

// write writes s to path, appending or truncating.
//
// It uses raw syscalls rather than os.File on purpose: cgroupfs (kernfs)
// files are pollable, so os.File registers them with Go's netpoller, and a
// write that fails with EAGAIN (memory.reclaim does this when it cannot
// reclaim the full amount) is then retried by waiting for POLLOUT forever
// instead of returning the error.
func write(path, s string, appendMode bool) error {
	flag := syscall.O_WRONLY | syscall.O_CREAT | syscall.O_CLOEXEC
	if appendMode {
		flag |= syscall.O_APPEND
	} else {
		flag |= syscall.O_TRUNC
	}
	fd, err := retryEINTR(func() (int, error) { return syscall.Open(path, flag, 0o644) })
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer syscall.Close(fd)
	b := []byte(s)
	for len(b) > 0 {
		n, err := retryEINTR(func() (int, error) { return syscall.Write(fd, b) })
		if err != nil {
			return &os.PathError{Op: "write", Path: path, Err: err}
		}
		if n <= 0 {
			return &os.PathError{Op: "write", Path: path, Err: io.ErrShortWrite}
		}
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

// writeFile is the writer used for all cgroup control-file writes; tests
// replace it to inject kernel errors.
var writeFile = write

// Delegated reports whether cg lies in the user's delegated subtree.
func (m Manager) Delegated(cg string) bool {
	return cg == m.Delegate || strings.HasPrefix(cg, m.Delegate+"/")
}

// Path returns the cgroup path for key.
func (m Manager) Path(key string) string { return m.Base + "/" + key }

// Exists reports whether cg is an existing directory.
func (m Manager) Exists(cg string) bool {
	fi, err := os.Stat(m.Root + cg)
	return err == nil && fi.IsDir()
}

// Ensure creates Base (with the memory controller enabled for its children)
// and the cgroup for key, returning Path(key).
func (m Manager) Ensure(key string) (string, error) {
	if key == "" || key == "." || key == ".." || strings.Contains(key, "/") {
		return "", fmt.Errorf("invalid cgroup key %q", key)
	}
	if err := os.MkdirAll(m.Root+m.Base, 0o755); err != nil {
		return "", err
	}
	if err := writeFile(m.Root+m.Base+"/cgroup.subtree_control", "+memory", false); err != nil {
		return "", fmt.Errorf("enable memory controller on %s: %w", m.Base, err)
	}
	cg := m.Path(key)
	if err := os.Mkdir(m.Root+cg, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	return cg, nil
}

// Move moves pid into cg. It returns ErrGone if the process no longer exists.
func (m Manager) Move(cg string, pid int) error {
	err := writeFile(m.Root+cg+"/cgroup.procs", strconv.Itoa(pid)+"\n", true)
	if errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("move pid %d to %s: %w", pid, cg, ErrGone)
	}
	if err != nil {
		return fmt.Errorf("move pid %d to %s: %w", pid, cg, err)
	}
	return nil
}

// Procs returns the PIDs listed in cg's cgroup.procs.
func (m Manager) Procs(cg string) ([]int, error) {
	b, err := os.ReadFile(m.Root + cg + "/cgroup.procs")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, f := range bytes.Fields(b) {
		pid, err := strconv.Atoi(string(f))
		if err != nil {
			return nil, fmt.Errorf("parse %s/cgroup.procs: %w", cg, err)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// SetFrozen writes cg's cgroup.freeze.
func (m Manager) SetFrozen(cg string, frozen bool) error {
	v := "0"
	if frozen {
		v = "1"
	}
	return writeFile(m.Root+cg+"/cgroup.freeze", v, false)
}

// Frozen reports whether cg's cgroup.events says "frozen 1".
func (m Manager) Frozen(cg string) (bool, error) {
	b, err := os.ReadFile(m.Root + cg + "/cgroup.events")
	if err != nil {
		return false, err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "frozen 1" {
			return true, nil
		}
	}
	return false, sc.Err()
}

// WaitFrozen polls Frozen every 20 ms until it equals want or timeout passes.
func (m Manager) WaitFrozen(cg string, want bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		got, err := m.Frozen(cg)
		if err != nil {
			return err
		}
		if got == want {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("cgroup %s did not reach frozen=%v within %s", cg, want, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Reclaim asks the kernel to reclaim up to n bytes from cg, preferring swap.
// If the kernel rejects swappiness= (EINVAL) it retries with plain bytes.
// EAGAIN (could not reclaim the full amount) is not an error.
func (m Manager) Reclaim(cg string, n uint64) error {
	if n == 0 {
		return nil
	}
	path := m.Root + cg + "/memory.reclaim"
	err := writeFile(path, fmt.Sprintf("%d swappiness=200", n), false)
	if errors.Is(err, syscall.EINVAL) {
		err = writeFile(path, fmt.Sprintf("%d", n), false)
	}
	if errors.Is(err, syscall.EAGAIN) {
		return nil
	}
	return err
}

// Keys returns the names of Base's child cgroups; nil, nil if Base is missing.
func (m Manager) Keys() ([]string, error) {
	ents, err := os.ReadDir(m.Root + m.Base)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, e := range ents {
		if e.IsDir() {
			keys = append(keys, e.Name())
		}
	}
	return keys, nil
}

// RemoveIfEmpty removes cg if it has no processes. A missing or busy cgroup
// is not an error.
func (m Manager) RemoveIfEmpty(cg string) error {
	pids, err := m.Procs(cg)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(pids) > 0 {
		return nil
	}
	dir := m.Root + cg
	err = os.Remove(dir)
	if errors.Is(err, syscall.ENOTEMPTY) {
		// Fake trees (tests) hold plain files; real cgroupfs never gets here.
		ents, rerr := os.ReadDir(dir)
		if rerr != nil {
			return rerr
		}
		for _, e := range ents {
			if e.Type().IsRegular() {
				if rerr := os.Remove(dir + "/" + e.Name()); rerr != nil {
					return rerr
				}
			}
		}
		err = os.Remove(dir)
	}
	if err == nil || errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.EBUSY) {
		return nil
	}
	return err
}
