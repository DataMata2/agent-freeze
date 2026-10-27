//go:build linux

package procfs_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/DataMata2/agent-freeze/internal/procfs"
	"github.com/DataMata2/agent-freeze/internal/procfs/procfstest"
)

func TestReadBasic(t *testing.T) {
	root := t.TempDir()
	procfstest.Write(t, root, procfstest.Fake{
		PID: 10, PPID: 1, StartTime: 4242, Comm: "claude",
		Exe: "/opt/claude/2.1/claude", Cwd: "/home/u/proj",
		Cgroup: "/user.slice/x.scope",
		Args:   []string{"claude", "-n", "Acme builder", "prompt with spaces"},
		RSSKB:  2048, SwapKB: 10,
	})
	p, err := procfs.FS{Root: root}.Read(10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := &procfs.Process{
		PID: 10, PPID: 1, StartTime: 4242, Comm: "claude",
		Exe: "/opt/claude/2.1/claude", Cwd: "/home/u/proj",
		Args: []string{"claude", "-n", "Acme builder", "prompt with spaces"},
		RSS:  2048 * 1024, Swap: 10 * 1024,
		Cgroup: "/user.slice/x.scope",
	}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("got %+v\nwant %+v", p, want)
	}
}

func TestReadCommWithParens(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "77")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Fields 5..21 are 17 zeros, so 999 is field 22 (starttime).
	stat := "77 (a) b (c)) S 5 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 999 0 0\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := procfs.FS{Root: root}.Read(77)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if p.PID != 77 || p.PPID != 5 || p.StartTime != 999 || p.Comm != "a) b (c)" {
		t.Fatalf("got %+v", p)
	}
}

func TestReadMalformedStat(t *testing.T) {
	for _, stat := range []string{"", "12 claude S 1", "12 (claude) S"} {
		root := t.TempDir()
		dir := filepath.Join(root, "12")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := (procfs.FS{Root: root}).Read(12); err == nil {
			t.Errorf("stat %q: expected error", stat)
		}
	}
}

func TestReadDeletedExe(t *testing.T) {
	root := t.TempDir()
	procfstest.Write(t, root, procfstest.Fake{PID: 20, PPID: 1, Comm: "codex", Exe: "/usr/bin/codex (deleted)"})
	p, err := procfs.FS{Root: root}.Read(20)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if p.Exe != "/usr/bin/codex" {
		t.Fatalf("Exe = %q", p.Exe)
	}
}

func TestReadCmdline(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"a\x00", []string{"a"}},
		{"a\x00b", []string{"a", "b"}},
		{"a\x00\x00", []string{"a", ""}},
	}
	for _, c := range cases {
		root := t.TempDir()
		procfstest.Write(t, root, procfstest.Fake{PID: 30, PPID: 1, Comm: "x"})
		if err := os.WriteFile(filepath.Join(root, "30", "cmdline"), []byte(c.raw), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := procfs.FS{Root: root}.Read(30)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if !reflect.DeepEqual(p.Args, c.want) {
			t.Errorf("cmdline %q: Args = %#v, want %#v", c.raw, p.Args, c.want)
		}
	}
}

func TestReadCgroupHybrid(t *testing.T) {
	root := t.TempDir()
	procfstest.Write(t, root, procfstest.Fake{PID: 40, PPID: 1, Comm: "x"})
	cg := "12:memory:/foo\n1:name=systemd:/bar\n0::/user.slice/y.scope\n"
	if err := os.WriteFile(filepath.Join(root, "40", "cgroup"), []byte(cg), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := procfs.FS{Root: root}.Read(40)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if p.Cgroup != "/user.slice/y.scope" {
		t.Fatalf("Cgroup = %q", p.Cgroup)
	}
}

func TestReadMissingOptionalFiles(t *testing.T) {
	root := t.TempDir()
	procfstest.Write(t, root, procfstest.Fake{
		PID: 50, PPID: 2, StartTime: 7, Comm: "kthread",
		Exe: "/bin/x", Cgroup: "/a", RSSKB: 5, SwapKB: 6,
	})
	for _, f := range []string{"status", "exe", "cgroup"} {
		if err := os.Remove(filepath.Join(root, "50", f)); err != nil {
			t.Fatal(err)
		}
	}
	p, err := procfs.FS{Root: root}.Read(50)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if p.PPID != 2 || p.StartTime != 7 || p.Comm != "kthread" {
		t.Fatalf("stat fields wrong: %+v", p)
	}
	if p.Exe != "" || p.Cgroup != "" || p.RSS != 0 || p.Swap != 0 || p.Cwd != "" || p.Args != nil {
		t.Fatalf("expected zero optional fields: %+v", p)
	}
}

func TestReadGone(t *testing.T) {
	_, err := procfs.FS{Root: t.TempDir()}.Read(999)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestSnapshotSkipsBroken(t *testing.T) {
	root := t.TempDir()
	procfstest.Write(t, root,
		procfstest.Fake{PID: 100, PPID: 1, Comm: "a"},
		procfstest.Fake{PID: 101, PPID: 100, Comm: "b"},
		procfstest.Fake{PID: 102, PPID: 100, Comm: "c"},
	)
	if err := os.Remove(filepath.Join(root, "101", "stat")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"self", "sys"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := procfs.FS{Root: root}.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 2 || snap[100] == nil || snap[102] == nil {
		t.Fatalf("snapshot = %v", snap)
	}
	if snap[102].Comm != "c" {
		t.Fatalf("snap[102] = %+v", snap[102])
	}
}

func TestSnapshotMissingRoot(t *testing.T) {
	_, err := procfs.FS{Root: filepath.Join(t.TempDir(), "nope")}.Snapshot()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMemInfo(t *testing.T) {
	root := t.TempDir()
	procfstest.WriteMemInfo(t, root, 16000000, 8000000, 4000000, 3000000)
	m, err := procfs.FS{Root: root}.MemInfo()
	if err != nil {
		t.Fatalf("MemInfo: %v", err)
	}
	want := procfs.MemInfo{
		MemTotal: 16000000 * 1024, MemAvailable: 8000000 * 1024,
		SwapTotal: 4000000 * 1024, SwapFree: 3000000 * 1024,
	}
	if m != want {
		t.Fatalf("got %+v, want %+v", m, want)
	}
}

func TestUptime(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "uptime"), []byte("12345.67 54321.00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := procfs.FS{Root: root}.Uptime()
	if err != nil {
		t.Fatalf("Uptime: %v", err)
	}
	if want := 12345670 * time.Millisecond; d != want {
		t.Fatalf("got %v, want %v", d, want)
	}
}

func TestNew(t *testing.T) {
	if r := procfs.New().Root; r != "/proc" {
		t.Fatalf("Root = %q", r)
	}
}
