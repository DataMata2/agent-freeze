//go:build linux

// Package procfstest writes fake /proc trees for tests.
package procfstest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Fake describes one fake /proc/<pid> entry.
type Fake struct {
	PID, PPID      int
	StartTime      uint64
	Comm, Exe, Cwd string // Exe and Cwd become symlinks; empty = no symlink
	Cgroup         string // written as "0::<Cgroup>\n"
	Args           []string
	RSSKB, SwapKB  uint64
}

// Write creates root/<pid>/{stat,status,cmdline,cgroup} and the exe/cwd
// symlinks for each fake process.
func Write(t testing.TB, root string, procs ...Fake) {
	t.Helper()
	for _, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(p.PID))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Fields 5..21 are zero, field 22 is starttime, then a few more zeros.
		stat := fmt.Sprintf("%d (%s) S %d%s %d 0 0 0\n",
			p.PID, p.Comm, p.PPID, strings.Repeat(" 0", 21-4), p.StartTime)
		status := fmt.Sprintf("Name:\t%s\nVmRSS:\t%8d kB\nVmSwap:\t%8d kB\n", p.Comm, p.RSSKB, p.SwapKB)
		var cmdline strings.Builder
		for _, a := range p.Args {
			cmdline.WriteString(a)
			cmdline.WriteByte(0)
		}
		files := map[string]string{
			"stat":    stat,
			"status":  status,
			"cmdline": cmdline.String(),
			"cgroup":  "0::" + p.Cgroup + "\n",
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for name, target := range map[string]string{"exe": p.Exe, "cwd": p.Cwd} {
			if target == "" {
				continue
			}
			if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// WriteMemInfo writes root/meminfo with the given kB values.
func WriteMemInfo(t testing.TB, root string, memTotalKB, memAvailKB, swapTotalKB, swapFreeKB uint64) {
	t.Helper()
	content := fmt.Sprintf("MemTotal:       %d kB\nMemFree:        0 kB\nMemAvailable:   %d kB\nSwapTotal:      %d kB\nSwapFree:       %d kB\n",
		memTotalKB, memAvailKB, swapTotalKB, swapFreeKB)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "meminfo"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
