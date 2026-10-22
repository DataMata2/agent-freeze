//go:build linux

// Package procfs reads the process information agent-freeze needs from a
// /proc-style directory tree.
package procfs

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ClockTicks is USER_HZ, the unit of Process.StartTime.
const ClockTicks = 100

// Process is one /proc/<pid> entry.
type Process struct {
	PID       int
	PPID      int
	StartTime uint64 // /proc/<pid>/stat field 22, clock ticks since boot
	Comm      string
	Exe       string   // readlink(/proc/<pid>/exe) with " (deleted)" stripped; "" if unreadable
	Args      []string // cmdline split on NUL, trailing empty element dropped; nil for kernel threads
	Cwd       string   // readlink(/proc/<pid>/cwd); "" if unreadable
	RSS       uint64   // VmRSS * 1024; 0 if absent
	Swap      uint64   // VmSwap * 1024; 0 if absent
	Cgroup    string   // path from the "0::" line of /proc/<pid>/cgroup; "" if none
}

// MemInfo holds system memory figures in bytes.
type MemInfo struct{ MemTotal, MemAvailable, SwapTotal, SwapFree uint64 }

// FS reads a /proc-style tree rooted at Root.
type FS struct{ Root string }

// New returns an FS for the real /proc.
func New() FS { return FS{Root: "/proc"} }

// Read reads one process. Only a missing or malformed stat file is an error;
// a missing stat yields an error wrapping fs.ErrNotExist. Every other file is
// optional and leaves its field zero when unreadable.
func (f FS) Read(pid int) (*Process, error) {
	dir := filepath.Join(f.Root, strconv.Itoa(pid))
	raw, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return nil, fmt.Errorf("procfs: read pid %d: %w", pid, err)
	}
	p, err := parseStat(raw)
	if err != nil {
		return nil, fmt.Errorf("procfs: pid %d: %w", pid, err)
	}
	p.PID = pid

	if b, err := os.ReadFile(filepath.Join(dir, "status")); err == nil {
		p.RSS, p.Swap = parseStatus(b)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		p.Args = parseCmdline(b)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cgroup")); err == nil {
		p.Cgroup = parseCgroup(b)
	}
	if s, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
		p.Exe = strings.TrimSuffix(s, " (deleted)")
	}
	if s, err := os.Readlink(filepath.Join(dir, "cwd")); err == nil {
		p.Cwd = s
	}
	return p, nil
}

// parseStat extracts comm, ppid and starttime. comm sits between the first
// '(' and the last ')' and may itself contain spaces and parentheses.
func parseStat(b []byte) (*Process, error) {
	open := bytes.IndexByte(b, '(')
	end := bytes.LastIndexByte(b, ')')
	if open < 0 || end < open {
		return nil, fmt.Errorf("malformed stat: no comm")
	}
	// rest[0] is field 3 (state), so field n is rest[n-3].
	rest := strings.Fields(string(b[end+1:]))
	if len(rest) < 22-2 {
		return nil, fmt.Errorf("malformed stat: %d fields after comm", len(rest))
	}
	ppid, err := strconv.Atoi(rest[4-3])
	if err != nil {
		return nil, fmt.Errorf("malformed stat ppid: %w", err)
	}
	start, err := strconv.ParseUint(rest[22-3], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("malformed stat starttime: %w", err)
	}
	return &Process{PPID: ppid, StartTime: start, Comm: string(b[open+1 : end])}, nil
}

func parseStatus(b []byte) (rss, swap uint64) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ":")
		if !ok || (key != "VmRSS" && key != "VmSwap") {
			continue
		}
		n := parseKB(val)
		if key == "VmRSS" {
			rss = n
		} else {
			swap = n
		}
	}
	return rss, swap
}

// parseKB parses "  1234 kB" into bytes; 0 on failure.
func parseKB(s string) uint64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return n * 1024
}

func parseCmdline(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	args := strings.Split(string(b), "\x00")
	if args[len(args)-1] == "" {
		args = args[:len(args)-1]
	}
	return args
}

func parseCgroup(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			return path
		}
	}
	return ""
}

// Snapshot reads every numeric entry of Root. Processes that vanish or
// cannot be read are skipped; only failure to list Root is an error.
func (f FS) Snapshot() (map[int]*Process, error) {
	entries, err := os.ReadDir(f.Root)
	if err != nil {
		return nil, fmt.Errorf("procfs: list %s: %w", f.Root, err)
	}
	out := make(map[int]*Process, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		p, err := f.Read(pid)
		if err != nil {
			continue
		}
		out[pid] = p
	}
	return out, nil
}

// MemInfo parses MemTotal, MemAvailable, SwapTotal and SwapFree from
// Root/meminfo.
func (f FS) MemInfo() (MemInfo, error) {
	b, err := os.ReadFile(filepath.Join(f.Root, "meminfo"))
	if err != nil {
		return MemInfo{}, fmt.Errorf("procfs: %w", err)
	}
	var m MemInfo
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		switch key {
		case "MemTotal":
			m.MemTotal = parseKB(val)
		case "MemAvailable":
			m.MemAvailable = parseKB(val)
		case "SwapTotal":
			m.SwapTotal = parseKB(val)
		case "SwapFree":
			m.SwapFree = parseKB(val)
		}
	}
	return m, nil
}

// Uptime returns the first field of Root/uptime.
func (f FS) Uptime() (time.Duration, error) {
	b, err := os.ReadFile(filepath.Join(f.Root, "uptime"))
	if err != nil {
		return 0, fmt.Errorf("procfs: %w", err)
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, fmt.Errorf("procfs: empty uptime")
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("procfs: uptime: %w", err)
	}
	return time.Duration(secs * float64(time.Second)).Round(time.Millisecond), nil
}
