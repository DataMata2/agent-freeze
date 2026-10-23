package procfs

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// swapVolume is where macOS creates swap files on demand.
	swapVolume = "/System/Volumes/VM"
	// lstartLayout is ps's lstart format with runs of spaces folded to one;
	// Claude Code writes it as procStart.
	lstartLayout = "Mon Jan 2 15:04:05 2006"
	// toolTimeout bounds ps and lsof, which can hang on a stale network mount.
	toolTimeout = 5 * time.Second
	// P_stat values from <sys/proc.h>.
	statStopped = 4
	statZombie  = 5
)

// FS reads the macOS process table, which has no /proc.
type FS struct{}

// New returns the FS of the running system.
func New() FS { return FS{} }

// Snapshot reads every process from the kernel. The executable path and
// arguments are read for the caller's own processes only: macOS refuses
// them for other users, and only own processes can be frozen. RSS and the
// terminal come from ps, the one tool that reports them without cgo.
func (FS) Snapshot() (map[int]*Process, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("procfs: list processes: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,rss=,tty=").Output()
	if err != nil {
		return nil, fmt.Errorf("procfs: ps: %w", err)
	}
	rows := parsePS(string(out))
	uid := os.Getuid()
	procs := make(map[int]*Process, len(kps))
	for i := range kps {
		kp := &kps[i]
		pid := int(kp.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		p := &Process{
			PID:       pid,
			PPID:      int(kp.Eproc.Ppid),
			PGID:      int(kp.Eproc.Pgid),
			UID:       int(kp.Eproc.Ucred.Uid),
			StartTime: uint64(kp.Proc.P_starttime.Sec)*1e6 + uint64(kp.Proc.P_starttime.Usec),
			Comm:      unix.ByteSliceToString(kp.Proc.P_comm[:]),
			State:     stateOf(kp.Proc.P_stat),
			RSS:       rows[pid].rss,
			TTY:       rows[pid].tty,
			// Tdev is -1 (NODEV) without a terminal.
			Foreground: kp.Eproc.Tdev != -1 && kp.Eproc.Tpgid == kp.Eproc.Pgid,
		}
		if p.UID == uid {
			if raw, err := unix.SysctlRaw("kern.procargs2", pid); err == nil {
				p.Exe, p.Args = parseProcArgs(raw)
			}
		}
		procs[pid] = p
	}
	return procs, nil
}

func stateOf(stat int8) byte {
	switch stat {
	case statStopped:
		return 'T'
	case statZombie:
		return 'Z'
	}
	return 'R'
}

// parseProcArgs splits a kern.procargs2 buffer: argc, the exec path, NUL
// padding, argv, then the environment. A malformed buffer yields no args.
func parseProcArgs(raw []byte) (exe string, args []string) {
	if len(raw) < 4 {
		return "", nil
	}
	argc := int(binary.LittleEndian.Uint32(raw))
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return "", nil
	}
	exe = string(rest[:end])
	rest = bytes.TrimLeft(rest[end:], "\x00")
	for len(args) < argc {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			break
		}
		args = append(args, string(rest[:end]))
		rest = rest[end+1:]
	}
	return exe, args
}

type psRow struct {
	rss uint64
	tty string
}

// parsePS reads "pid rss-kB tty" lines; a tty of "??" means none.
func parsePS(out string) map[int]psRow {
	rows := make(map[int]psRow)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		kb, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		r := psRow{rss: kb * 1024}
		if f[2] != "??" && f[2] != "-" {
			r.tty = "/dev/" + f[2]
		}
		rows[pid] = r
	}
	return rows
}

// Cwds returns the working directory of those pids lsof can read. Like a
// missing /proc/<pid>/cwd on Linux, an unreadable one is left out.
func (FS) Cwds(pids []int) map[int]string {
	if len(pids) == 0 {
		return map[int]string{}
	}
	list := make([]string, len(pids))
	for i, pid := range pids {
		list[i] = strconv.Itoa(pid)
	}
	// lsof exits 1 when one of the pids has no match; its output still counts.
	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "/usr/sbin/lsof", "-a", "-d", "cwd", "-p", strings.Join(list, ","), "-Fn", "-w").Output()
	return parseLsofCwd(string(out))
}

// parseLsofCwd reads lsof -Fn output: a "p<pid>" line, then "n<path>".
func parseLsofCwd(out string) map[int]string {
	cwds := make(map[int]string)
	pid := 0
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "n") && pid > 0:
			cwds[pid] = line[1:]
		}
	}
	return cwds
}

// MemInfo reads total RAM, the share the kernel counts as free, and swap.
// macOS grows swap files on demand, so the free space of the swap volume
// counts as free swap.
func (FS) MemInfo() (MemInfo, error) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return MemInfo{}, fmt.Errorf("procfs: hw.memsize: %w", err)
	}
	level, err := unix.SysctlUint32("kern.memorystatus_level")
	if err != nil {
		return MemInfo{}, fmt.Errorf("procfs: kern.memorystatus_level: %w", err)
	}
	raw, err := unix.SysctlRaw("vm.swapusage")
	if err != nil {
		return MemInfo{}, fmt.Errorf("procfs: vm.swapusage: %w", err)
	}
	swapTotal, swapAvail, ok := parseSwapUsage(raw)
	if !ok {
		return MemInfo{}, fmt.Errorf("procfs: vm.swapusage: %d bytes", len(raw))
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(swapVolume, &fs); err != nil {
		return MemInfo{}, fmt.Errorf("procfs: statfs %s: %w", swapVolume, err)
	}
	disk := fs.Bavail * uint64(fs.Bsize)
	return MemInfo{
		MemTotal:     total,
		MemAvailable: total / 100 * uint64(level),
		SwapTotal:    swapTotal + disk,
		SwapFree:     swapAvail + disk,
	}, nil
}

// parseSwapUsage reads the total and available bytes of a struct xsw_usage.
func parseSwapUsage(raw []byte) (total, avail uint64, ok bool) {
	if len(raw) < 16 {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint64(raw), binary.LittleEndian.Uint64(raw[8:]), true
}

// SameStart reports whether procStart, the start time Claude Code writes in
// a session file, is p's start time. On macOS that is ps's lstart text,
// which Claude Code writes in UTC; local time and the decimal StartTime are
// accepted too.
func SameStart(p *Process, procStart string) bool {
	if procStart == strconv.FormatUint(p.StartTime, 10) {
		return true
	}
	s := strings.Join(strings.Fields(procStart), " ")
	for _, loc := range []*time.Location{time.UTC, time.Local} {
		t, err := time.ParseInLocation(lstartLayout, s, loc)
		if err == nil && t.Unix() == int64(p.StartTime/1e6) {
			return true
		}
	}
	return false
}
