package discover

import (
	"path"
	"testing"
	"time"

	"github.com/DataMata2/agent-freeze/internal/procfs"
	"github.com/DataMata2/agent-freeze/internal/sessions"
)

func proc(pid, ppid int, tty string, args ...string) *procfs.Process {
	return &procfs.Process{PID: pid, PPID: ppid, Args: args, Comm: path.Base(args[0]), TTY: tty}
}

func TestGroupsCarryTheAgentTerminal(t *testing.T) {
	procs := map[int]*procfs.Process{
		1:  proc(1, 0, "", "launchd"),
		10: proc(10, 1, "/dev/ttys003", "claude"),
		20: proc(20, 1, "", "claude", "--bg-pty-host"),
		21: proc(21, 20, "/dev/ttys007", "claude"),
	}
	got := map[int]string{}
	for _, g := range Groups(procs, 0) {
		got[g.RootPID] = g.TTY
	}
	want := map[int]string{10: "/dev/ttys003", 20: "/dev/ttys007"}
	if len(got) != len(want) || got[10] != want[10] || got[20] != want[20] {
		t.Fatalf("TTY by root = %v, want %v", got, want)
	}
}

func TestEnrichSetsStatusSinceFromTheSessionFile(t *testing.T) {
	procs := map[int]*procfs.Process{10: proc(10, 1, "", "claude"), 11: proc(11, 1, "", "claude")}
	groups := []Group{{Agent: "claude", RootPID: 10}, {Agent: "claude", RootPID: 11}}
	Enrich(groups, procs, map[int]sessions.Info{
		10: {PID: 10, Status: "idle", StatusUpdatedAt: 1792800000000},
		11: {PID: 11, Status: "busy"},
	})
	if s := groups[0].StatusSince; s == nil || !s.Equal(time.UnixMilli(1792800000000)) {
		t.Errorf("StatusSince = %v, want %v", s, time.UnixMilli(1792800000000))
	}
	if groups[1].StatusSince != nil {
		t.Errorf("StatusSince without a timestamp = %v, want nil", groups[1].StatusSince)
	}
}

func TestNamesLoseControlCharacters(t *testing.T) {
	if got := collapse("evil\x1b]0;owned\x07 name\n"); got != "evil ]0;owned name" {
		t.Fatalf("collapse = %q", got)
	}
}
