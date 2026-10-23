package freezer

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/DataMata2/agent-freeze/internal/discover"
	"github.com/DataMata2/agent-freeze/internal/procfs"
	"github.com/DataMata2/agent-freeze/internal/prog"
	"github.com/DataMata2/agent-freeze/internal/sessions"
	"github.com/DataMata2/agent-freeze/internal/state"
)

// stopPoll is how often a freeze re-reads the process table while waiting
// for processes to stop.
const stopPoll = 10 * time.Millisecond

// shells are the programs whose jobs an agent can be.
var shells = []string{"zsh", "bash", "fish", "sh", "dash", "ksh", "tcsh", "csh", "nu"}

var errAgentExited = errors.New("agent exited")

// System hooks; tests replace them with a fake process table.
var (
	snapshot = procfs.New().Snapshot
	cwds     = procfs.New().Cwds
	memInfo  = procfs.New().MemInfo
	kill     = syscall.Kill
)

// Freezer implements API on macOS, which has no cgroup freezer: it stops
// the group with SIGSTOP and continues it with SIGCONT. An agent run from
// an interactive shell is a job of that shell, and a stopped job makes the
// shell print "suspended" and take the terminal back. So the shell is
// stopped first and continued last: by the time it runs again its job runs
// too, it never notices, and the tab never needs fg. The memory of a stopped
// group is not reclaimed at once; macOS compresses or swaps it out as soon
// as other apps need RAM, and Freed grows as it does.
type Freezer struct {
	Store       state.Store
	SelfPID     int
	UID         int
	SessionsDir string // sessions.DefaultDir() in production; "" means no enrichment
	StopTimeout time.Duration
	Now         func() time.Time
	// Banner shows a "frozen" banner in the agent's terminal while frozen
	// and clears it on thaw (New: off when AFZ_NO_BANNER=1).
	Banner bool
}

var _ API = (*Freezer)(nil)

// New returns the production Freezer for uid, excluding selfPID from every
// group.
func New(uid, selfPID int) *Freezer {
	return &Freezer{
		Store:       state.Store{Path: state.DefaultPath()},
		SelfPID:     selfPID,
		UID:         uid,
		SessionsDir: sessions.DefaultDir(),
		StopTimeout: 5 * time.Second,
		Now:         time.Now,
		Banner:      os.Getenv("AFZ_NO_BANNER") != "1",
	}
}

// groups reads the process table, the agents' working directories and the
// Claude session files.
func (f *Freezer) groups() (map[int]*procfs.Process, []discover.Group, error) {
	procs, err := snapshot()
	if err != nil {
		return nil, nil, err
	}
	var agents []int
	for pid, p := range procs {
		if discover.AgentOf(p) != "" {
			agents = append(agents, pid)
		}
	}
	for pid, dir := range cwds(agents) {
		if p, ok := procs[pid]; ok {
			p.Cwd = dir
		}
	}
	groups := discover.Groups(procs, f.SelfPID)
	if f.SessionsDir != "" {
		infos, _ := sessions.Load(f.SessionsDir)
		discover.Enrich(groups, procs, infos)
	}
	return procs, groups, nil
}

// List returns every discovered group with its freeze state. A group is
// frozen or partial only if afz froze it: an agent stopped with Ctrl+Z is
// its shell's business. A group whose freeze was cut short after its shell
// stopped is partial, so thaw finds it. List also ends the freezes that are
// over (see housekeep).
func (f *Freezer) List() ([]GroupView, error) {
	procs, groups, err := f.groups()
	if err != nil {
		return nil, err
	}
	ents := f.housekeep(procs)
	views := make([]GroupView, 0, len(groups))
	for _, g := range groups {
		v := GroupView{Group: g, State: "running", Delegated: len(g.PIDs) > 0}
		live, stopped := 0, 0
		for _, pid := range g.PIDs {
			p := procs[pid]
			if p.UID != f.UID {
				v.Delegated = false
			}
			if p.State == 'Z' {
				continue
			}
			live++
			if p.State == 'T' {
				stopped++
			}
		}
		if e, ok := ents[g.Key]; ok && (stopped > 0 || stoppedShell(procs, e)) {
			v.State = "partial"
			if stopped == live {
				v.State = "frozen"
			}
			if !e.FrozenAt.IsZero() {
				at := e.FrozenAt
				v.FrozenAt = &at
			}
			if e.RSSBefore > g.RSS {
				v.Freed = e.RSSBefore - g.RSS
			}
		}
		views = append(views, v)
	}
	return views, nil
}

// housekeep ends the freezes that are over (see freezeOver) and returns the
// other entries: it continues every process such a freeze stopped, the shell
// last, and drops its entry. procs may predate a freeze that has just
// finished, so a freeze it shows as over is checked again against a fresh
// snapshot taken under the state lock, which every freeze step holds too.
// Errors leave the entries as they are.
func (f *Freezer) housekeep(procs map[int]*procfs.Process) map[string]state.Entry {
	ents, err := f.Store.Load()
	if err != nil {
		return nil
	}
	if !slices.ContainsFunc(slices.Collect(maps.Values(ents)), func(e state.Entry) bool { return freezeOver(procs, e) }) {
		return ents
	}
	var kept map[string]state.Entry
	err = f.Store.Update(func(m map[string]state.Entry) error {
		now, err := snapshot()
		if err != nil {
			return err
		}
		for k, e := range m {
			if freezeOver(now, e) {
				f.resume(now, e)
				delete(m, k)
			}
		}
		kept = maps.Clone(m)
		return nil
	})
	if err != nil {
		return ents
	}
	return kept
}

// freezeOver reports whether the freeze e describes has ended: its agent
// exited (killed while frozen, its helpers would stay stopped with nothing
// left to thaw), or the freeze finished and nothing it stopped but the shell
// is stopped any more (continued by hand). A freeze still in progress (no
// FrozenAt) is not over while its agent lives.
func freezeOver(procs map[int]*procfs.Process, e state.Entry) bool {
	root, alive := procs[e.RootPID]
	if !alive || root.StartTime != e.Starts[e.RootPID] {
		return true
	}
	return !e.FrozenAt.IsZero() && !stoppedMember(procs, e)
}

// stoppedMember reports whether a process e stopped, other than its shell,
// is still stopped.
func stoppedMember(procs map[int]*procfs.Process, e state.Entry) bool {
	for pid, start := range e.Starts {
		if p, ok := procs[pid]; ok && pid != e.Shell && p.StartTime == start && p.State == 'T' {
			return true
		}
	}
	return false
}

// stoppedShell reports whether the shell e stopped is still stopped.
func stoppedShell(procs map[int]*procfs.Process, e state.Entry) bool {
	p, ok := procs[e.Shell]
	return ok && e.Shell != 0 && p.StartTime == e.Starts[e.Shell] && p.State == 'T'
}

// resume sends SIGCONT to every process e stopped that still has its
// recorded start time (PID reuse guard): the helpers, then the agent, then
// the shell. A process that has not reached the stopped state yet gets it
// too, since SIGCONT cancels a pending SIGSTOP and does nothing to a running
// process. It returns the signalling errors as warnings.
func (f *Freezer) resume(procs map[int]*procfs.Process, e state.Entry) []string {
	var helpers []int
	for pid := range e.Starts {
		if pid != e.RootPID && pid != e.Shell {
			helpers = append(helpers, pid)
		}
	}
	slices.Sort(helpers)
	slices.Reverse(helpers)
	var warnings []string
	for _, pid := range append(helpers, e.RootPID, e.Shell) {
		p, ok := procs[pid]
		if !ok || p.StartTime != e.Starts[pid] {
			continue
		}
		if err := kill(pid, syscall.SIGCONT); err != nil && !errors.Is(err, syscall.ESRCH) {
			warnings = append(warnings, fmt.Sprintf("continue pid %d: %v", pid, err))
		}
	}
	return warnings
}

// Freeze stops g's shell, if g is its foreground job, and then every member
// of g.
func (f *Freezer) Freeze(g GroupView, forceSelf bool) Result {
	r := Result{ID: g.ID, Key: g.Key, Name: g.Name}
	if g.Self && !forceSelf {
		r.Error = "refusing to freeze the agent that is running " + prog.Name + " (use --force-self)"
		return r
	}
	if g.State == "frozen" {
		r.OK, r.Warning = true, "already frozen"
		return r
	}
	procs, err := snapshot()
	if err != nil {
		r.Error = err.Error()
		return r
	}
	root, ok := procs[g.RootPID]
	if !ok || (g.StartTime != 0 && root.StartTime != g.StartTime) {
		r.Error = errAgentExited.Error()
		return r
	}
	members := discover.Members(procs, g.RootPID, f.SelfPID)
	for _, pid := range members {
		if procs[pid].UID != f.UID {
			r.Error = fmt.Sprintf("process %d belongs to another user", pid)
			return r
		}
		r.RSSBefore += procs[pid].RSS
	}

	rec := state.Entry{
		Key: g.Key, Name: g.Name, Agent: g.Agent, Cwd: g.Cwd, RootPID: g.RootPID,
		Starts: map[int]uint64{g.RootPID: root.StartTime},
	}
	procs, err = f.stop(g, root, procs, &rec)
	if err != nil {
		return f.rollback(r, rec, err)
	}
	for _, pid := range discover.Members(procs, g.RootPID, f.SelfPID) {
		r.RSSAfter += procs[pid].RSS
	}
	var warnings []string
	rec.FrozenAt, rec.RSSBefore = f.Now(), r.RSSBefore
	if err := f.record(rec); err != nil {
		warnings = append(warnings, "state: "+err.Error())
	}
	if f.Banner {
		if p := bannerProc(procs, procs[g.RootPID]); p != nil && p.TTY != "" {
			if err := showBanner(p.TTY, g.Name, rec.FrozenAt); err != nil {
				warnings = append(warnings, "banner: "+err.Error())
			}
		}
	}
	r.OK = true
	r.Warning = strings.Join(warnings, "; ")
	return r
}

// stop stops root's shell, if root is its foreground job, waits until it is
// stopped, then stops every member of g, re-reading the process table for
// processes spawned meanwhile, until all of them are stopped. Every process
// goes into rec, and rec into the state file, before it is signalled, so
// thaw and housekeep always know what to continue. It returns the last
// process table.
func (f *Freezer) stop(g GroupView, root *procfs.Process, procs map[int]*procfs.Process, rec *state.Entry) (map[int]*procfs.Process, error) {
	deadline := time.Now().Add(f.StopTimeout)
	if shell := jobShell(procs, root); shell != 0 {
		rec.Shell = shell
		rec.Starts[shell] = procs[shell].StartTime
		if err := f.record(*rec); err != nil {
			return procs, err
		}
		switch err := kill(shell, syscall.SIGSTOP); {
		case errors.Is(err, syscall.ESRCH):
			// The shell just exited: no job control is left to hide from.
		case err != nil:
			return procs, fmt.Errorf("stop shell %d: %w", shell, err)
		default:
			var err error
			if procs, err = waitStopped(shell, deadline); err != nil {
				return procs, err
			}
		}
	}
	signalled := make(map[int]bool)
	for {
		if p, ok := procs[g.RootPID]; !ok || p.StartTime != root.StartTime {
			return procs, errAgentExited
		}
		var pending []int
		waiting := 0
		for _, pid := range discover.Members(procs, g.RootPID, f.SelfPID) {
			p := procs[pid]
			if p.State == 'Z' {
				continue
			}
			if !signalled[pid] {
				pending = append(pending, pid)
			}
			if p.State != 'T' {
				waiting++
			}
		}
		if waiting == 0 && len(pending) == 0 {
			return procs, nil
		}
		if len(pending) > 0 {
			for _, pid := range pending {
				rec.Starts[pid] = procs[pid].StartTime
			}
			if err := f.record(*rec); err != nil {
				return procs, err
			}
			for _, pid := range pending {
				signalled[pid] = true
				if err := kill(pid, syscall.SIGSTOP); err != nil && !errors.Is(err, syscall.ESRCH) {
					return procs, fmt.Errorf("stop pid %d: %w", pid, err)
				}
			}
		} else if time.Now().After(deadline) {
			return procs, fmt.Errorf("%d processes did not stop", waiting)
		}
		time.Sleep(stopPoll)
		var err error
		if procs, err = snapshot(); err != nil {
			return procs, err
		}
	}
}

// waitStopped waits until pid is stopped or gone and returns the process
// table that shows it.
func waitStopped(pid int, deadline time.Time) (map[int]*procfs.Process, error) {
	for {
		procs, err := snapshot()
		if err != nil {
			return procs, err
		}
		if p, ok := procs[pid]; !ok || p.State == 'T' {
			return procs, nil
		}
		if time.Now().After(deadline) {
			return procs, fmt.Errorf("shell %d did not stop", pid)
		}
		time.Sleep(stopPoll)
	}
}

// record writes rec to the state file. Start times recorded before under
// the same key, by this freeze or a concurrent one, are kept.
func (f *Freezer) record(rec state.Entry) error {
	return f.Store.Update(func(m map[string]state.Entry) error {
		m[rec.Key] = merge(m[rec.Key], rec)
		return nil
	})
}

// merge returns rec with the start times and shell of old added.
func merge(old, rec state.Entry) state.Entry {
	starts := maps.Clone(old.Starts)
	if starts == nil {
		starts = make(map[int]uint64, len(rec.Starts))
	}
	maps.Copy(starts, rec.Starts)
	rec.Starts = starts
	if rec.Shell == 0 {
		rec.Shell = old.Shell
	}
	return rec
}

// jobShell returns the interactive shell root is the foreground job of, or
// 0. A background job (started with "&") is not one: its shell is at the
// prompt, and stopping it would stop the user's typing.
func jobShell(procs map[int]*procfs.Process, root *procfs.Process) int {
	parent, ok := procs[root.PPID]
	if !ok || !root.Foreground || root.PGID == parent.PGID || parent.UID != root.UID || !isShell(parent) {
		return 0
	}
	return parent.PID
}

// isShell reports whether p runs a shell, login shells ("-zsh") included.
func isShell(p *procfs.Process) bool {
	names := []string{path.Base(p.Exe)}
	if len(p.Args) > 0 {
		names = append(names, strings.TrimPrefix(path.Base(p.Args[0]), "-"))
	}
	return slices.ContainsFunc(names, func(n string) bool { return slices.Contains(shells, n) })
}

// rollback undoes a partial freeze, continuing what rec and the state file
// say it stopped, and returns r failed with err.
func (f *Freezer) rollback(r Result, rec state.Entry, err error) Result {
	r.Error = "freeze failed: " + err.Error()
	if errors.Is(err, errAgentExited) {
		r.Error = err.Error()
	}
	ents, _ := f.Store.Load()
	warnings, terr := f.thaw(merge(ents[rec.Key], rec))
	if terr != nil {
		r.Error += " (rollback: " + terr.Error() + ")"
	}
	r.Warning = strings.Join(warnings, "; ")
	return r
}

// thaw continues every process e stopped and deletes the entry for e.Key.
func (f *Freezer) thaw(e state.Entry) ([]string, error) {
	procs, err := snapshot()
	if err != nil {
		return nil, err
	}
	warnings := f.resume(procs, e)
	err = f.Store.Update(func(m map[string]state.Entry) error {
		delete(m, e.Key)
		return nil
	})
	if err != nil {
		warnings = append(warnings, "state: "+err.Error())
	}
	return warnings, nil
}

// Thaw continues g: its helpers, the agent, then its shell.
func (f *Freezer) Thaw(g GroupView) Result {
	r := Result{ID: g.ID, Key: g.Key, Name: g.Name, RSSBefore: g.RSS, RSSAfter: g.RSS}
	ents, err := f.Store.Load()
	if err != nil {
		r.Error = err.Error()
		return r
	}
	e, ok := ents[g.Key]
	if !ok {
		r.OK, r.Warning = true, "not frozen"
		return r
	}
	warnings, err := f.thaw(e)
	if err != nil {
		r.Error = "thaw failed: " + err.Error()
		return r
	}
	if f.Banner {
		warnings = append(warnings, f.clearFrozen(g)...)
	}
	r.OK = true
	r.Warning = strings.Join(warnings, "; ")
	return r
}

// clearFrozen clears the freeze banner from g's terminal and sends the agent
// SIGWINCH so it redraws its screen. It returns "banner: ..." warnings.
func (f *Freezer) clearFrozen(g GroupView) []string {
	procs, err := snapshot()
	if err != nil {
		return []string{"banner: " + err.Error()}
	}
	root, ok := procs[g.RootPID]
	if !ok || (g.StartTime != 0 && root.StartTime != g.StartTime) {
		return nil
	}
	p := bannerProc(procs, root)
	if p == nil || p.TTY == "" {
		return nil
	}
	var warnings []string
	if err := clearBanner(p.TTY, g.Name); err != nil {
		warnings = append(warnings, "banner: "+err.Error())
	}
	if err := sendWinch(p.PID); err != nil && !errors.Is(err, syscall.ESRCH) {
		warnings = append(warnings, "banner: "+err.Error())
	}
	return warnings
}

// Status totals the frozen and partial groups and reads system memory.
func (f *Freezer) Status() (Status, error) {
	views, err := f.List()
	if err != nil {
		return Status{}, err
	}
	var s Status
	for _, v := range views {
		if v.State == "frozen" || v.State == "partial" {
			s.Frozen++
			s.Freed += v.Freed
			s.FrozenSwap += v.Swap
		}
	}
	mi, err := memInfo()
	if err != nil {
		return Status{}, err
	}
	s.MemAvailable, s.SwapFree = mi.MemAvailable, mi.SwapFree
	return s, nil
}
