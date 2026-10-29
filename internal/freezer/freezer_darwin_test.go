package freezer

import (
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DataMata2/agent-freeze/internal/procfs"
	"github.com/DataMata2/agent-freeze/internal/state"
)

const (
	mib  = 1 << 20
	tty1 = "/dev/ttys001"
	tty3 = "/dev/ttys003"
)

var frozenTime = time.Date(2026, 10, 23, 15, 4, 0, 0, time.UTC)

type sent struct {
	pid int
	sig syscall.Signal
}

// world is a fake macOS process table that applies the signals sent to it.
type world struct {
	procs    map[int]*procfs.Process
	sent     []sent
	stuck    map[int]bool // processes that never reach the stopped state
	lagging  map[int]bool // processes whose SIGSTOP takes effect at the next snapshot
	gone     map[int]bool // processes that exit just before they are signalled
	pending  map[int]bool
	stale    map[int]*procfs.Process // returned once by the next snapshot
	onSignal func(w *world, pid int, sig syscall.Signal)
	banner   []string
}

func p(pid, ppid, pgid int, tty, exe string, args ...string) *procfs.Process {
	return &procfs.Process{
		PID: pid, PPID: ppid, PGID: pgid, UID: 501, StartTime: uint64(pid) * 1000,
		Comm: path.Base(exe), Exe: exe, Args: args, RSS: 100 * mib, State: 'R', TTY: tty,
	}
}

func fg(p *procfs.Process) *procfs.Process {
	p.Foreground = true
	return p
}

func newWorld() *world {
	w := &world{procs: map[int]*procfs.Process{}, stuck: map[int]bool{}, lagging: map[int]bool{}, gone: map[int]bool{}, pending: map[int]bool{}}
	for _, p := range []*procfs.Process{
		{PID: 1, Comm: "launchd", Exe: "/sbin/launchd", Args: []string{"/sbin/launchd"}, State: 'R'},
		// A Terminal tab: claude is the foreground job of an interactive zsh.
		p(100, 1, 100, "", "/System/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal"),
		p(101, 100, 101, tty1, "/usr/bin/login", "login", "-pf", "u"),
		p(102, 101, 102, tty1, "/bin/zsh", "-zsh"),
		fg(p(103, 102, 103, tty1, "/Users/u/.local/bin/claude", "claude")),
		fg(p(104, 103, 103, tty1, "/opt/homebrew/bin/node", "node", "/Users/u/mcp/server.js")),
		fg(p(105, 104, 103, tty1, "/bin/sh", "sh", "-c", "sleep 100")),
		// VS Code runs claude without a terminal; this afz runs from its tool shell.
		p(200, 1, 200, "", "/Applications/Visual Studio Code.app/Contents/MacOS/Code"),
		p(201, 200, 200, "", "/Users/u/.vscode/extensions/anthropic.claude-code/resources/native-binary/claude", "claude", "--output-format", "stream-json"),
		p(202, 201, 200, "", "/bin/zsh", "/bin/zsh", "-c", "afz list"),
		p(203, 202, 200, "", "/Users/u/go/bin/afz", "afz", "list"),
		// A wrapper script runs codex in its own process group: no job of a shell.
		p(301, 100, 301, "/dev/ttys002", "/usr/bin/login", "login", "-pf", "u"),
		p(302, 301, 302, "/dev/ttys002", "/bin/zsh", "-zsh"),
		fg(p(303, 302, 303, "/dev/ttys002", "/bin/bash", "bash", "/Users/u/bin/wrap")),
		fg(p(304, 303, 303, "/dev/ttys002", "/opt/homebrew/bin/codex", "codex")),
		// Another user's claude: its arguments are unreadable.
		{PID: 400, PPID: 1, PGID: 400, UID: 502, StartTime: 400000, Comm: "claude", State: 'R', RSS: 50 * mib},
		// claude started with "&": a background job, the shell owns the terminal.
		p(501, 100, 501, tty3, "/usr/bin/login", "login", "-pf", "u"),
		fg(p(502, 501, 502, tty3, "/bin/zsh", "-zsh")),
		p(503, 502, 503, tty3, "/Users/u/.local/bin/claude", "claude", "-n", "bg job"),
	} {
		w.procs[p.PID] = p
	}
	return w
}

func (w *world) snapshot() (map[int]*procfs.Process, error) {
	for pid := range w.pending {
		w.procs[pid].State = 'T'
		delete(w.pending, pid)
	}
	src := w.procs
	if w.stale != nil {
		src, w.stale = w.stale, nil
	}
	out := make(map[int]*procfs.Process, len(src))
	for pid, p := range src {
		c := *p
		c.Args = slices.Clone(p.Args)
		out[pid] = &c
	}
	return out, nil
}

func (w *world) kill(pid int, sig syscall.Signal) error {
	p, ok := w.procs[pid]
	if !ok || w.gone[pid] {
		return syscall.ESRCH
	}
	w.sent = append(w.sent, sent{pid, sig})
	switch {
	case sig == syscall.SIGSTOP && w.lagging[pid]:
		w.pending[pid] = true
	case sig == syscall.SIGSTOP && !w.stuck[pid] && p.State != 'Z':
		p.State = 'T'
	case sig == syscall.SIGCONT && p.State == 'T':
		p.State = 'R'
	}
	if w.onSignal != nil {
		w.onSignal(w, pid, sig)
	}
	return nil
}

// signalled returns the pids sent sig, in order.
func (w *world) signalled(sig syscall.Signal) []int {
	var out []int
	for _, s := range w.sent {
		if s.sig == sig {
			out = append(out, s.pid)
		}
	}
	return out
}

func (w *world) states(pids ...int) string {
	var b strings.Builder
	for _, pid := range pids {
		b.WriteByte(w.procs[pid].State)
	}
	return b.String()
}

func newFreezer(t *testing.T, w *world) *Freezer {
	t.Helper()
	oldSnap, oldKill, oldCwds, oldMem := snapshot, kill, cwds, memInfo
	oldShow, oldClear, oldWinch := showBanner, clearBanner, sendWinch
	t.Cleanup(func() {
		snapshot, kill, cwds, memInfo = oldSnap, oldKill, oldCwds, oldMem
		showBanner, clearBanner, sendWinch = oldShow, oldClear, oldWinch
	})
	snapshot, kill = w.snapshot, w.kill
	cwds = func(pids []int) map[int]string {
		all := map[int]string{103: "/Users/u/acme", 201: "/Users/u/api", 304: "/Users/u/web", 503: "/Users/u/bg"}
		out := map[int]string{}
		for _, pid := range pids {
			if d, ok := all[pid]; ok {
				out[pid] = d
			}
		}
		return out
	}
	memInfo = func() (procfs.MemInfo, error) {
		return procfs.MemInfo{MemTotal: 16 << 30, MemAvailable: 6 << 30, SwapTotal: 160 << 30, SwapFree: 150 << 30}, nil
	}
	showBanner = func(tty, name string, at time.Time) error {
		w.banner = append(w.banner, "show "+tty+" "+name+" "+at.Format("15:04"))
		return nil
	}
	clearBanner = func(tty, name string) error {
		w.banner = append(w.banner, "clear "+tty+" "+name)
		return nil
	}
	sendWinch = func(pid int) error { return w.kill(pid, syscall.SIGWINCH) }
	return &Freezer{
		Store:       state.Store{Path: filepath.Join(t.TempDir(), "state.json")},
		SelfPID:     203,
		UID:         501,
		StopTimeout: 50 * time.Millisecond,
		Now:         func() time.Time { return frozenTime },
		Banner:      true,
	}
}

func view(t *testing.T, f *Freezer, root int) GroupView {
	t.Helper()
	views, err := f.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, v := range views {
		if v.RootPID == root {
			return v
		}
	}
	t.Fatalf("no group with root %d in %+v", root, views)
	return GroupView{}
}

func entries(t *testing.T, f *Freezer) map[string]state.Entry {
	t.Helper()
	m, err := f.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDarwinListGroups(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	views, err := f.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	roots := map[int]GroupView{}
	for _, v := range views {
		roots[v.RootPID] = v
	}
	if got := slices.Sorted(maps.Keys(roots)); !slices.Equal(got, []int{103, 201, 304, 400, 503}) {
		t.Fatalf("roots %v", got)
	}
	if g := roots[103]; g.State != "running" || !g.Delegated || g.Cwd != "/Users/u/acme" || g.Name != "claude @ acme" ||
		!slices.Equal(g.PIDs, []int{103, 104, 105}) || g.RSS != 300*mib {
		t.Errorf("terminal claude: %+v", g)
	}
	if g := roots[201]; !g.Self || slices.Contains(g.PIDs, 203) {
		t.Errorf("self group: %+v", g)
	}
	if g := roots[400]; g.Delegated {
		t.Errorf("another user's group is delegated: %+v", g)
	}
}

func TestDarwinFreezeStopsTheShellFirst(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	r := f.Freeze(view(t, f, 103), false)
	if !r.OK || r.Error != "" || r.RSSBefore != 300*mib {
		t.Fatalf("result %+v", r)
	}
	if got := w.signalled(syscall.SIGSTOP); len(got) != 4 || got[0] != 102 || !slices.Equal(slices.Sorted(slices.Values(got[1:])), []int{103, 104, 105}) {
		t.Fatalf("SIGSTOP order %v, want the shell 102 first, then 103 104 105", got)
	}
	if s := w.states(102, 103, 104, 105, 101, 100); s != "TTTTRR" {
		t.Fatalf("states %s", s)
	}
	v := view(t, f, 103)
	if v.State != "frozen" || v.FrozenAt == nil || !v.FrozenAt.Equal(frozenTime) {
		t.Fatalf("after freeze: %+v", v)
	}
	e := entries(t, f)[v.Key]
	if e.Shell != 102 || e.RootPID != 103 || e.RSSBefore != 300*mib || len(e.Starts) != 4 || e.Starts[105] != 105000 {
		t.Fatalf("entry %+v", e)
	}
	if !slices.Equal(w.banner, []string{"show " + tty1 + " claude @ acme 15:04"}) {
		t.Fatalf("banner %q", w.banner)
	}
}

func TestDarwinThawWakesTheTreeThenTheShell(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	if r := f.Freeze(view(t, f, 103), false); !r.OK {
		t.Fatalf("freeze %+v", r)
	}
	w.sent = nil
	r := f.Thaw(view(t, f, 103))
	if !r.OK || r.Warning != "" {
		t.Fatalf("thaw %+v", r)
	}
	got := w.signalled(syscall.SIGCONT)
	if len(got) != 4 || got[2] != 103 || got[3] != 102 {
		t.Fatalf("SIGCONT order %v, want helpers, then 103, then the shell 102", got)
	}
	if s := w.states(102, 103, 104, 105); s != "RRRR" {
		t.Fatalf("states %s", s)
	}
	if !slices.Equal(w.signalled(syscall.SIGWINCH), []int{103}) || w.banner[len(w.banner)-1] != "clear "+tty1+" claude @ acme" {
		t.Fatalf("winch %v banner %q", w.signalled(syscall.SIGWINCH), w.banner)
	}
	if len(entries(t, f)) != 0 || view(t, f, 103).State != "running" {
		t.Fatal("still frozen after thaw")
	}
}

func TestDarwinFreezeLeavesOtherParentsRunning(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	for _, root := range []int{304, 503} {
		if r := f.Freeze(view(t, f, root), false); !r.OK {
			t.Fatalf("freeze %d: %+v", root, r)
		}
	}
	if got := w.signalled(syscall.SIGSTOP); !slices.Equal(got, []int{304, 503}) {
		t.Fatalf("SIGSTOP %v: a wrapper script or a shell with a background job must keep running", got)
	}
	if e := entries(t, f)[view(t, f, 503).Key]; e.Shell != 0 {
		t.Fatalf("background job recorded shell %d", e.Shell)
	}
}

func TestDarwinFreezeRefusals(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	if r := f.Freeze(view(t, f, 201), false); r.OK || !strings.Contains(r.Error, "--force-self") {
		t.Errorf("self: %+v", r)
	}
	if r := f.Freeze(view(t, f, 400), false); r.OK || !strings.Contains(r.Error, "another user") {
		t.Errorf("other user: %+v", r)
	}
	gone := view(t, f, 503)
	delete(w.procs, 503)
	if r := f.Freeze(gone, false); r.OK || r.Error != "agent exited" {
		t.Errorf("gone: %+v", r)
	}
	reused := view(t, f, 304)
	w.procs[304].StartTime++
	if r := f.Freeze(reused, false); r.OK || r.Error != "agent exited" {
		t.Errorf("pid reused: %+v", r)
	}
	if len(w.sent) != 0 || len(entries(t, f)) != 0 {
		t.Fatalf("refusals signalled %v or left state %v", w.sent, entries(t, f))
	}
}

func TestDarwinForceSelfSparesAfz(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	if r := f.Freeze(view(t, f, 201), true); !r.OK {
		t.Fatalf("freeze %+v", r)
	}
	if s := w.states(201, 202, 203, 200); s != "TTRR" {
		t.Fatalf("states %s: afz and VS Code must keep running", s)
	}
}

func TestDarwinFreezeAlreadyFrozen(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	f.Freeze(view(t, f, 103), false)
	w.sent = nil
	if r := f.Freeze(view(t, f, 103), false); !r.OK || r.Warning != "already frozen" || len(w.sent) != 0 {
		t.Fatalf("result %+v sent %v", r, w.sent)
	}
}

func TestDarwinFreezeTimeoutRollsBack(t *testing.T) {
	w := newWorld()
	w.stuck[105] = true
	f := newFreezer(t, w)
	r := f.Freeze(view(t, f, 103), false)
	if r.OK || !strings.Contains(r.Error, "1 processes did not stop") {
		t.Fatalf("result %+v", r)
	}
	if s := w.states(102, 103, 104, 105); s != "RRRR" {
		t.Fatalf("states %s after rollback", s)
	}
	if got := w.signalled(syscall.SIGCONT); got[len(got)-1] != 102 || !slices.Contains(got, 105) {
		t.Fatalf("SIGCONT %v: 105 was sent SIGSTOP and must be continued too, the shell last", got)
	}
	if len(entries(t, f)) != 0 || len(w.banner) != 0 {
		t.Fatalf("rollback left state %v banner %q", entries(t, f), w.banner)
	}
}

func TestDarwinFreezeStopsChildrenSpawnedMeanwhile(t *testing.T) {
	w := newWorld()
	w.onSignal = func(w *world, pid int, sig syscall.Signal) {
		if pid == 104 && sig == syscall.SIGSTOP && w.procs[106] == nil {
			w.procs[106] = fg(p(106, 103, 103, tty1, "/bin/zsh", "zsh", "-c", "git status"))
		}
	}
	f := newFreezer(t, w)
	if r := f.Freeze(view(t, f, 103), false); !r.OK {
		t.Fatalf("freeze %+v", r)
	}
	if w.procs[106].State != 'T' || entries(t, f)[view(t, f, 103).Key].Starts[106] != 106000 {
		t.Fatalf("late child: state %c, recorded %v", w.procs[106].State, entries(t, f))
	}
}

func TestDarwinFreezeIgnoresZombies(t *testing.T) {
	w := newWorld()
	w.procs[105].State = 'Z'
	f := newFreezer(t, w)
	if r := f.Freeze(view(t, f, 103), false); !r.OK {
		t.Fatalf("freeze %+v", r)
	}
	if v := view(t, f, 103); v.State != "frozen" {
		t.Fatalf("state %s", v.State)
	}
}

func TestDarwinStoppedWithoutFreezeIsRunning(t *testing.T) {
	w := newWorld()
	for _, pid := range []int{103, 104, 105} {
		w.procs[pid].State = 'T' // the user pressed Ctrl+Z
	}
	f := newFreezer(t, w)
	v := view(t, f, 103)
	if v.State != "running" {
		t.Fatalf("state %s, want running: only groups afz froze count as frozen", v.State)
	}
	if r := f.Thaw(v); !r.OK || r.Warning != "not frozen" || len(w.sent) != 0 {
		t.Fatalf("thaw %+v sent %v", r, w.sent)
	}
}

func TestDarwinAgentExitedWhileFrozen(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	f.Freeze(view(t, f, 103), false)
	delete(w.procs, 103) // killed while frozen; its helpers stay stopped
	w.procs[104].PPID = 1
	w.sent = nil
	if _, err := f.List(); err != nil {
		t.Fatal(err)
	}
	if s := w.states(102, 104, 105); s != "RRR" {
		t.Fatalf("states %s: helpers and the shell must not stay stopped", s)
	}
	if got := w.signalled(syscall.SIGCONT); got[len(got)-1] != 102 {
		t.Fatalf("SIGCONT %v: the shell must be woken last", got)
	}
	if len(entries(t, f)) != 0 {
		t.Fatalf("state kept: %v", entries(t, f))
	}
}

func TestDarwinThawedOutsideAfz(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	f.Freeze(view(t, f, 103), false)
	for _, pid := range []int{103, 104, 105} {
		w.procs[pid].State = 'R' // kill -CONT by hand
	}
	if v := view(t, f, 103); v.State != "running" || w.procs[102].State != 'R' || len(entries(t, f)) != 0 {
		t.Fatalf("state %s, shell %c, entries %v", v.State, w.procs[102].State, entries(t, f))
	}
}

func TestDarwinFreedGrowsAsMemoryIsPagedOut(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	if r := f.Freeze(view(t, f, 103), false); !r.OK || r.RSSAfter != r.RSSBefore {
		t.Fatalf("freeze %+v: nothing is freed at once on macOS", r)
	}
	w.procs[103].RSS = 10 * mib
	w.procs[104].RSS = 40 * mib
	if v := view(t, f, 103); v.Freed != 150*mib {
		t.Fatalf("freed %d MiB, want 150", v.Freed/mib)
	}
	st, err := f.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Frozen != 1 || st.Freed != 150*mib || st.MemAvailable != 6<<30 || st.SwapFree != 150<<30 {
		t.Fatalf("status %+v", st)
	}
}

func TestDarwinNoBannerWithoutTerminal(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	f.Freeze(view(t, f, 201), true)
	f.Thaw(view(t, f, 201))
	if len(w.banner) != 0 || len(w.signalled(syscall.SIGWINCH)) != 0 {
		t.Fatalf("banner %q winch %v for a group without a terminal", w.banner, w.signalled(syscall.SIGWINCH))
	}
}

func TestDarwinStaleListKeepsALiveFreeze(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	before, _ := w.snapshot()
	if r := f.Freeze(view(t, f, 103), false); !r.OK {
		t.Fatalf("freeze %+v", r)
	}
	w.stale = before // a List that read the process table just before the freeze ended
	if _, err := f.List(); err != nil {
		t.Fatal(err)
	}
	if len(entries(t, f)) != 1 || w.states(102, 103) != "TT" || view(t, f, 103).State != "frozen" {
		t.Fatalf("a stale List ended the freeze: entries %v states %s", entries(t, f), w.states(102, 103))
	}
}

func TestDarwinShellIsRecordedWithTheAgent(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	v := view(t, f, 103)
	w.onSignal = func(w *world, pid int, sig syscall.Signal) {
		if pid == 102 && sig == syscall.SIGSTOP {
			if e := entries(t, f)[v.Key]; e.Starts[102] != 102000 || e.Starts[103] != 103000 {
				t.Errorf("entry when the shell stops: %+v", e)
			}
		}
	}
	if r := f.Freeze(v, false); !r.OK {
		t.Fatalf("freeze %+v", r)
	}
}

func TestDarwinWaitsForTheShellToStop(t *testing.T) {
	w := newWorld()
	w.lagging[102] = true
	w.onSignal = func(w *world, pid int, sig syscall.Signal) {
		if pid != 102 && sig == syscall.SIGSTOP && w.procs[102].State != 'T' {
			t.Errorf("pid %d stopped before its shell", pid)
		}
	}
	f := newFreezer(t, w)
	if r := f.Freeze(view(t, f, 103), false); !r.OK {
		t.Fatalf("freeze %+v", r)
	}
}

func TestDarwinShellGoneStillFreezes(t *testing.T) {
	w := newWorld()
	w.gone[102] = true
	f := newFreezer(t, w)
	if r := f.Freeze(view(t, f, 103), false); !r.OK || w.states(103, 104, 105) != "TTT" {
		t.Fatalf("freeze %+v, states %s", r, w.states(103, 104, 105))
	}
}

func TestDarwinInterruptedFreezeIsThawable(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	v := view(t, f, 103)
	w.procs[102].State = 'T' // afz was killed right after it stopped the shell
	err := f.Store.Update(func(m map[string]state.Entry) error {
		m[v.Key] = state.Entry{Key: v.Key, RootPID: 103, Shell: 102, Starts: map[int]uint64{102: 102000, 103: 103000}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := view(t, f, 103).State; got != "partial" {
		t.Fatalf("state %s, want partial so that thaw --all finds the stopped shell", got)
	}
	if r := f.Thaw(view(t, f, 103)); !r.OK || w.procs[102].State != 'R' || len(entries(t, f)) != 0 {
		t.Fatalf("thaw %+v, shell %c", r, w.procs[102].State)
	}
}

func TestDarwinThawDuringFreezeKeepsAFullEntry(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	v := view(t, f, 103)
	w.onSignal = func(w *world, pid int, sig syscall.Signal) {
		if pid == 105 && sig == syscall.SIGSTOP {
			_ = f.Store.Update(func(m map[string]state.Entry) error { delete(m, v.Key); return nil })
		}
	}
	if r := f.Freeze(v, false); !r.OK {
		t.Fatalf("freeze %+v", r)
	}
	if e := entries(t, f)[v.Key]; e.RootPID != 103 || e.Shell != 102 || len(e.Starts) != 4 {
		t.Fatalf("entry %+v: thaw could not continue what is stopped", e)
	}
}

func TestDarwinAgentExitsDuringFreeze(t *testing.T) {
	w := newWorld()
	w.onSignal = func(w *world, pid int, sig syscall.Signal) {
		if pid == 104 && sig == syscall.SIGSTOP {
			delete(w.procs, 103)
		}
	}
	f := newFreezer(t, w)
	r := f.Freeze(view(t, f, 103), false)
	if r.OK || r.Error != "agent exited" || w.states(102, 104, 105) != "RRR" || len(entries(t, f)) != 0 {
		t.Fatalf("result %+v, states %s, entries %v", r, w.states(102, 104, 105), entries(t, f))
	}
}

func TestDarwinFreezeAsRecordsBy(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	if r := f.FreezeAs(view(t, f, 103), false, "auto"); !r.OK {
		t.Fatalf("FreezeAs = %+v", r)
	}
	v := view(t, f, 103)
	if v.State != "frozen" || v.FrozenBy != "auto" || entries(t, f)[v.Key].By != "auto" {
		t.Fatalf("after auto freeze: %+v", v)
	}
	// A manual freeze of the auto-frozen group takes it over.
	if r := f.Freeze(v, false); !r.OK || r.Warning != "already frozen" {
		t.Fatalf("Freeze = %+v", r)
	}
	if v = view(t, f, 103); v.FrozenBy != "" {
		t.Fatalf("FrozenBy %q after manual freeze", v.FrozenBy)
	}
	if r := f.Thaw(v); !r.OK {
		t.Fatalf("Thaw = %+v", r)
	}
	if _, ok := entries(t, f)[v.Key]; ok {
		t.Fatal("state entry not deleted")
	}
}

func TestDarwinFreezeIsManual(t *testing.T) {
	w := newWorld()
	f := newFreezer(t, w)
	f.Freeze(view(t, f, 103), false)
	if v := view(t, f, 103); v.State != "frozen" || v.FrozenBy != "" {
		t.Fatalf("after freeze: %+v", v)
	}
}
