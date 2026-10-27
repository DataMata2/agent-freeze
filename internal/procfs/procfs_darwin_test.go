package procfs

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// procArgs builds a kern.procargs2 buffer: argc, the exec path, NUL
// padding, then argv and the environment, each NUL-terminated.
func procArgs(argc int, exe string, strs ...string) []byte {
	b := binary.LittleEndian.AppendUint32(nil, uint32(argc))
	b = append(b, exe...)
	b = append(b, 0, 0, 0, 0)
	for _, s := range strs {
		b = append(b, s...)
		b = append(b, 0)
	}
	return b
}

func TestParseProcArgs(t *testing.T) {
	raw := procArgs(4, "/opt/claude/claude", "claude", "-n", "Acme builder", "", "HOME=/Users/u")
	exe, args := parseProcArgs(raw)
	if exe != "/opt/claude/claude" || !reflect.DeepEqual(args, []string{"claude", "-n", "Acme builder", ""}) {
		t.Fatalf("got exe=%q args=%q", exe, args)
	}
}

func TestParseProcArgsBroken(t *testing.T) {
	for _, raw := range [][]byte{nil, {1, 0}, procArgs(0, "/bin/x"), {1, 0, 0, 0, '/', 'x'}} {
		if exe, args := parseProcArgs(raw); args != nil {
			t.Errorf("%q: got exe=%q args=%q, want no args", raw, exe, args)
		}
	}
	// argc larger than the strings present keeps what is there.
	if _, args := parseProcArgs(procArgs(5, "/bin/x", "x", "-v")); !reflect.DeepEqual(args, []string{"x", "-v"}) {
		t.Fatalf("short argv: got %q", args)
	}
}

func TestParsePS(t *testing.T) {
	rows := parsePS("    1  12345 ??\n  501   2048 ttys003\nnot a row\n  77 x ttys001\n")
	want := map[int]psRow{
		1:   {rss: 12345 * 1024},
		501: {rss: 2048 * 1024, tty: "/dev/ttys003"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("got %+v\nwant %+v", rows, want)
	}
}

func TestParseLsofCwd(t *testing.T) {
	got := parseLsofCwd("p501\nfcwd\nn/Users/u/proj\np777\nfcwd\nn/Users/u/My Folder\np9\nfcwd\n")
	want := map[int]string{501: "/Users/u/proj", 777: "/Users/u/My Folder"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestParseSwapUsage(t *testing.T) {
	raw := binary.LittleEndian.AppendUint64(nil, 8<<30)
	raw = binary.LittleEndian.AppendUint64(raw, 920<<20)
	raw = binary.LittleEndian.AppendUint64(raw, 8<<30-920<<20)
	raw = binary.LittleEndian.AppendUint32(raw, 16384)
	raw = binary.LittleEndian.AppendUint32(raw, 1)
	total, avail, ok := parseSwapUsage(raw)
	if !ok || total != 8<<30 || avail != 920<<20 {
		t.Fatalf("got total=%d avail=%d ok=%v", total, avail, ok)
	}
	if _, _, ok := parseSwapUsage(raw[:10]); ok {
		t.Fatal("short buffer accepted")
	}
}

func TestSameStart(t *testing.T) {
	start := time.Date(2026, 10, 3, 7, 5, 52, 123456000, time.UTC)
	p := &Process{StartTime: uint64(start.UnixMicro())}
	for s, want := range map[string]bool{
		"Sat Oct  3 07:05:52 2026":                              true, // Claude Code writes ps's lstart in UTC
		start.In(time.Local).Format("Mon Jan _2 15:04:05 2006"): true,
		strconv.FormatUint(p.StartTime, 10):                     true,
		"Sat Oct  3 07:05:53 2026":                              false,
		"yesterday":                                             false,
	} {
		if got := SameStart(p, s); got != want {
			t.Errorf("SameStart(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestSnapshotFindsItself(t *testing.T) {
	procs, err := New().Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	me := procs[os.Getpid()]
	if me == nil {
		t.Fatal("own process missing")
	}
	if me.PPID != os.Getppid() || me.UID != os.Getuid() || me.PGID != syscall.Getpgrp() || me.State != 'R' {
		t.Errorf("got %+v", me)
	}
	if !reflect.DeepEqual(me.Args, os.Args) || filepath.Base(me.Exe) != filepath.Base(os.Args[0]) {
		t.Errorf("exe=%q args=%q, want args %q", me.Exe, me.Args, os.Args)
	}
	if age := time.Since(time.UnixMicro(int64(me.StartTime))); me.RSS == 0 || age < 0 || age > time.Hour {
		t.Errorf("rss=%d start age=%v", me.RSS, age)
	}
}

func TestSnapshotSeesStoppedProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.Dir = t.TempDir()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	var p *Process
	for range 50 {
		procs, err := New().Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if p = procs[cmd.Process.Pid]; p != nil && p.State == 'T' {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p == nil || p.State != 'T' || p.PPID != os.Getpid() || !reflect.DeepEqual(p.Args, []string{"sleep", "30"}) {
		t.Fatalf("got %+v", p)
	}
	dir, _ := filepath.EvalSymlinks(cmd.Dir)
	if got := New().Cwds([]int{cmd.Process.Pid}); got[cmd.Process.Pid] != dir {
		t.Fatalf("Cwds = %v, want %q", got, dir)
	}
}

func TestMemInfo(t *testing.T) {
	m, err := New().MemInfo()
	if err != nil {
		t.Fatalf("MemInfo: %v", err)
	}
	if m.MemTotal == 0 || m.MemAvailable == 0 || m.MemAvailable > m.MemTotal || m.SwapFree == 0 || m.SwapFree > m.SwapTotal {
		t.Fatalf("got %+v", m)
	}
}
