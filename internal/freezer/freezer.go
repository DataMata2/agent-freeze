//go:build linux

package freezer

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/DataMata2/agent-freeze/internal/banner"
	"github.com/DataMata2/agent-freeze/internal/cgroup"
	"github.com/DataMata2/agent-freeze/internal/discover"
	"github.com/DataMata2/agent-freeze/internal/procfs"
	"github.com/DataMata2/agent-freeze/internal/prog"
	"github.com/DataMata2/agent-freeze/internal/sessions"
	"github.com/DataMata2/agent-freeze/internal/state"
)

const (
	// maxMovePasses bounds the re-scan loop that migrates members spawned
	// while the group was being moved.
	maxMovePasses = 5
	// maxReclaimRounds bounds the iterative reclaim after a freeze.
	maxReclaimRounds = 4
	// reclaimDonePercent stops reclaim once the remaining RSS is below this
	// share of the RSS before the freeze.
	reclaimDonePercent = 5
	// minRoundProgress stops reclaim when a round freed less than this.
	minRoundProgress = 16 << 20
	// housekeepMinAge keeps housekeeping from removing an empty key cgroup
	// younger than this: a concurrent Freeze may have just created it.
	housekeepMinAge = 30 * time.Second
	// inProgressMaxAge bounds how long a state entry without FrozenAt (a
	// freeze in progress) protects its empty key cgroup from housekeeping.
	inProgressMaxAge = 10 * time.Minute
)

// afterReclaimRound, when set (by tests only), is called after each reclaim
// round with the 1-based round number.
var afterReclaimRound func(round int)

// afterMove, when set (by tests only), is called after every CG.Move with
// its arguments and result, so tests can simulate the kernel or change the
// fake /proc between passes.
var afterMove func(cg string, pid int, err error)

// ttyOf finds an agent's terminal; tests replace it (see shared.go for the
// other banner hooks).
var ttyOf = banner.TTY

// move moves pid into cg and reports the call to afterMove.
func (f *Freezer) move(cg string, pid int) error {
	err := f.CG.Move(cg, pid)
	if afterMove != nil {
		afterMove(cg, pid, err)
	}
	return err
}

// Freezer implements API on top of procfs, discover, cgroup and state.
type Freezer struct {
	Proc          procfs.FS
	CG            cgroup.Manager
	Store         state.Store
	SelfPID       int
	SessionsDir   string // sessions.DefaultDir() in production; "" means no enrichment
	FreezeTimeout time.Duration
	Now           func() time.Time
	// Banner shows a "frozen" banner in the agent's terminal while frozen
	// and clears it on thaw (New: off when AFZ_NO_BANNER=1).
	Banner bool
}

var _ API = (*Freezer)(nil)

// New returns the production Freezer for uid, excluding selfPID from every
// group.
func New(uid, selfPID int) *Freezer {
	return &Freezer{
		Proc:          procfs.New(),
		CG:            cgroup.New(uid),
		Store:         state.Store{Path: state.DefaultPath()},
		SelfPID:       selfPID,
		SessionsDir:   sessions.DefaultDir(),
		FreezeTimeout: 5 * time.Second,
		Now:           time.Now,
		Banner:        os.Getenv("AFZ_NO_BANNER") != "1",
	}
}

// List returns every discovered group with its freeze state. It also removes
// empty agent-freeze cgroups and prunes state entries whose cgroup is gone;
// those housekeeping errors are ignored.
func (f *Freezer) List() ([]GroupView, error) {
	procs, err := f.Proc.Snapshot()
	if err != nil {
		return nil, err
	}
	groups := discover.Groups(procs, f.SelfPID)
	if f.SessionsDir != "" {
		infos, _ := sessions.Load(f.SessionsDir)
		discover.Enrich(groups, procs, infos)
	}

	f.housekeep()
	ents, err := f.Store.Load()
	if err != nil {
		ents = nil
	}

	views := make([]GroupView, 0, len(groups))
	for _, g := range groups {
		v := GroupView{Group: g, State: "running", Delegated: len(g.PIDs) > 0}
		cg := f.CG.Path(g.Key)
		inside := true
		for _, pid := range g.PIDs {
			c := procs[pid].Cgroup
			if !f.CG.Delegated(c) {
				v.Delegated = false
			}
			if c != cg {
				inside = false
			}
		}
		if f.CG.Exists(cg) {
			if frozen, err := f.CG.Frozen(cg); err == nil && frozen {
				v.State = "partial"
				if inside {
					v.State = "frozen"
				}
			}
		}
		if v.State != "running" {
			if e, ok := ents[g.Key]; ok {
				if !e.FrozenAt.IsZero() {
					at := e.FrozenAt
					v.FrozenAt = &at
				}
				v.Freed = e.Freed
				v.FrozenBy = e.By
			}
		}
		if p := bannerProc(procs, procs[g.RootPID]); p != nil {
			v.TTY = ttyOf(f.Proc, p.PID)
		}
		views = append(views, v)
	}
	return views, nil
}

// housekeep removes empty agent-freeze cgroups and drops state entries whose
// cgroup no longer exists. A key cgroup younger than housekeepMinAge is kept,
// as is one younger than inProgressMaxAge whose state entry has no FrozenAt
// yet (a freeze in progress). Errors are ignored.
func (f *Freezer) housekeep() {
	ents, err := f.Store.Load()
	keys, _ := f.CG.Keys()
	for _, k := range keys {
		cg := f.CG.Path(k)
		fi, serr := os.Stat(f.CG.Root + cg)
		if serr != nil {
			continue
		}
		age := f.Now().Sub(fi.ModTime())
		if age < housekeepMinAge {
			continue
		}
		if e, ok := ents[k]; ok && e.FrozenAt.IsZero() && age < inProgressMaxAge {
			continue
		}
		_ = f.CG.RemoveIfEmpty(cg)
	}
	if err != nil {
		return
	}
	stale := false
	for _, e := range ents {
		if !f.CG.Exists(e.Cgroup) {
			stale = true
			break
		}
	}
	if !stale {
		return // avoid creating or rewriting the state file for nothing
	}
	_ = f.Store.Update(func(m map[string]state.Entry) error {
		for k, e := range m {
			if !f.CG.Exists(e.Cgroup) {
				delete(m, k)
			}
		}
		return nil
	})
}

// Freeze freezes g manually; see FreezeAs.
func (f *Freezer) Freeze(g GroupView, forceSelf bool) Result {
	return f.FreezeAs(g, forceSelf, "")
}

// FreezeAs moves g's members into its own cgroup, freezes it and reclaims
// the members' memory, recording by ("auto" or "" for manual) as who froze
// it (see owner and claimManual).
func (f *Freezer) FreezeAs(g GroupView, forceSelf bool, by string) Result {
	r := Result{ID: g.ID, Key: g.Key, Name: g.Name}
	if g.Self && !forceSelf {
		r.Error = "refusing to freeze the agent that is running " + prog.Name + " (use --force-self)"
		return r
	}
	if g.State == "frozen" {
		r.OK, r.Warning = true, "already frozen"
		if by == "" {
			if err := claimManual(f.Store, g.Key); err != nil {
				r.Warning += "; state: " + err.Error()
			}
		}
		return r
	}

	procs, err := f.Proc.Snapshot()
	if err != nil {
		r.Error = err.Error()
		return r
	}
	root, ok := procs[g.RootPID]
	if !ok || (g.StartTime != 0 && root.StartTime != g.StartTime) {
		r.Error = "agent exited"
		return r
	}
	first := procs
	members := discover.Members(procs, g.RootPID, f.SelfPID)
	for _, pid := range members {
		if c := procs[pid].Cgroup; !f.CG.Delegated(c) {
			r.Error = "not in user-delegated cgroup: " + c
			return r
		}
		r.RSSBefore += procs[pid].RSS
	}

	cg, err := f.CG.Ensure(g.Key)
	if err != nil {
		r.Error = "freeze failed: " + err.Error()
		return r
	}

	// Move members in, re-scanning for processes spawned meanwhile.
	var origins map[int]string
	moved := make(map[int]bool)
	for pass := 0; pass < maxMovePasses; pass++ {
		if pass > 0 {
			if procs, err = f.Proc.Snapshot(); err != nil {
				return f.rollback(r, g, "freeze failed: "+err.Error())
			}
			// The root may have exited (and its PID been reused) meanwhile.
			if p, ok := procs[g.RootPID]; !ok || p.StartTime != root.StartTime {
				return f.rollback(r, g, "agent exited")
			}
			members = discover.Members(procs, g.RootPID, f.SelfPID)
		}
		var pending []int
		for _, pid := range members {
			if c := procs[pid].Cgroup; c != cg && !moved[pid] {
				if !f.CG.Delegated(c) {
					return f.rollback(r, g, "not in user-delegated cgroup: "+c)
				}
				pending = append(pending, pid)
			}
		}
		if len(pending) == 0 {
			break
		}
		// Record origins before moving, so a crash never leaves a moved PID
		// without one.
		err := f.Store.Update(func(m map[string]state.Entry) error {
			e := m[g.Key]
			e.Key, e.Name, e.Agent, e.Cwd, e.Cgroup = g.Key, g.Name, g.Agent, g.Cwd, cg
			if e.Origins == nil {
				e.Origins = make(map[int]string)
			}
			for _, pid := range pending {
				if _, ok := e.Origins[pid]; !ok {
					e.Origins[pid] = procs[pid].Cgroup
				}
			}
			m[g.Key] = e
			origins = e.Origins
			return nil
		})
		if err != nil {
			return f.rollback(r, g, "freeze failed: "+err.Error())
		}
		for _, pid := range pending {
			moved[pid] = true
			if err := f.move(cg, pid); err != nil && !errors.Is(err, cgroup.ErrGone) {
				return f.rollback(r, g, "freeze failed: "+err.Error())
			}
		}
	}

	if err := f.CG.SetFrozen(cg, true); err != nil {
		return f.rollback(r, g, "freeze failed: "+err.Error())
	}
	if err := f.CG.WaitFrozen(cg, true, f.FreezeTimeout); err != nil {
		return f.rollback(r, g, "freeze failed: "+err.Error())
	}

	var warnings []string
	if errs := f.reclaim(cg, members, origins, first, r.RSSBefore); len(errs) > 0 {
		warnings = append(warnings, "reclaim: "+strings.Join(errs, "; "))
	}

	r.RSSAfter, r.SwapAfter = f.measure(members)
	freed := uint64(0)
	if r.RSSBefore > r.RSSAfter {
		freed = r.RSSBefore - r.RSSAfter
	}
	frozenAt := f.Now()
	err = f.Store.Update(func(m map[string]state.Entry) error {
		e := m[g.Key]
		e.Key, e.Name, e.Agent, e.Cwd, e.Cgroup = g.Key, g.Name, g.Agent, g.Cwd, cg
		e.By = owner(e, by)
		e.FrozenAt, e.RSSBefore, e.Freed = frozenAt, r.RSSBefore, freed
		m[g.Key] = e
		return nil
	})
	if err != nil {
		warnings = append(warnings, "state: "+err.Error())
	}
	if f.Banner {
		if err := f.showFrozen(procs, g, frozenAt); err != nil {
			warnings = append(warnings, "banner: "+err.Error())
		}
	}
	r.OK = true
	r.Warning = strings.Join(warnings, "; ")
	return r
}

// rollback undoes a partial freeze of g and returns r failed with msg.
func (f *Freezer) rollback(r Result, g GroupView, msg string) Result {
	r.Error = msg
	warnings, err := f.thaw(g.Key, g.RootPID)
	if err != nil {
		r.Error += " (rollback: " + err.Error() + ")"
	}
	r.Warning = strings.Join(warnings, "; ")
	return r
}

// reclaim pushes the frozen members' memory out in up to maxReclaimRounds
// rounds. Each round reclaims the members' remaining RSS from their origin
// cgroups (where their pages stay charged) and from cg. It returns the
// reclaim errors.
func (f *Freezer) reclaim(cg string, members []int, origins map[int]string, first map[int]*procfs.Process, before uint64) []string {
	originOf := func(pid int) string {
		if o := origins[pid]; o != "" {
			return o
		}
		if p, ok := first[pid]; ok {
			return p.Cgroup
		}
		return ""
	}
	var errs []string
	remaining, byPID := f.rss(members)
	for round := 1; round <= maxReclaimRounds; round++ {
		if remaining == 0 || remaining*100 < before*reclaimDonePercent {
			break
		}
		byOrigin := make(map[string]uint64)
		for pid, n := range byPID {
			if o := originOf(pid); o != "" && o != cg {
				byOrigin[o] += n
			}
		}
		names := make([]string, 0, len(byOrigin))
		for o := range byOrigin {
			names = append(names, o)
		}
		slices.Sort(names)
		for _, o := range names {
			if !f.CG.Exists(o) || !f.CG.Delegated(o) {
				continue
			}
			if err := f.CG.Reclaim(o, byOrigin[o]); err != nil {
				errs = append(errs, err.Error())
			}
		}
		if err := f.CG.Reclaim(cg, remaining); err != nil {
			errs = append(errs, err.Error())
		}
		if afterReclaimRound != nil {
			afterReclaimRound(round)
		}
		next, nextByPID := f.rss(members)
		progress := uint64(0)
		if remaining > next {
			progress = remaining - next
		}
		remaining, byPID = next, nextByPID
		if progress < minRoundProgress {
			break
		}
	}
	return errs
}

// rss re-reads the VmRSS of pids, returning the total and the per-PID values
// of the processes still alive.
func (f *Freezer) rss(pids []int) (uint64, map[int]uint64) {
	var total uint64
	by := make(map[int]uint64, len(pids))
	for _, pid := range pids {
		p, err := f.Proc.Read(pid)
		if err != nil {
			continue
		}
		by[pid] = p.RSS
		total += p.RSS
	}
	return total, by
}

// measure re-reads pids and sums their VmRSS and VmSwap.
func (f *Freezer) measure(pids []int) (rss, swap uint64) {
	for _, pid := range pids {
		p, err := f.Proc.Read(pid)
		if err != nil {
			continue
		}
		rss += p.RSS
		swap += p.Swap
	}
	return rss, swap
}

// Thaw unfreezes g, moves its processes back to their origin cgroups and
// removes its cgroup and state entry.
func (f *Freezer) Thaw(g GroupView) Result {
	r := Result{ID: g.ID, Key: g.Key, Name: g.Name, RSSBefore: g.RSS}
	if !f.CG.Exists(f.CG.Path(g.Key)) {
		r.OK, r.Warning = true, "not frozen"
		return r
	}
	pids, _ := f.CG.Procs(f.CG.Path(g.Key))
	warnings, err := f.thaw(g.Key, g.RootPID)
	r.Warning = strings.Join(warnings, "; ")
	if err != nil {
		r.Error = err.Error()
		return r
	}
	if f.Banner {
		warnings = append(warnings, f.clearFrozen(g)...)
		r.Warning = strings.Join(warnings, "; ")
	}
	r.RSSAfter, r.SwapAfter = f.measure(pids)
	r.OK = true
	return r
}

// showFrozen writes the freeze banner to the terminal of g's banner process
// in procs. No terminal means no banner and no error.
func (f *Freezer) showFrozen(procs map[int]*procfs.Process, g GroupView, at time.Time) error {
	p := bannerProc(procs, procs[g.RootPID])
	if p == nil {
		return nil
	}
	tty := ttyOf(f.Proc, p.PID)
	if tty == "" {
		return nil
	}
	return showBanner(tty, g.Name, at)
}

// clearFrozen clears the freeze banner from the terminal of g's banner
// process and then sends that process SIGWINCH so it redraws. It must run
// only after the group is thawed. The signal is sent only if the process
// still has the start time read before the clear (PID reuse guard). It
// returns "banner: ..." warnings.
func (f *Freezer) clearFrozen(g GroupView) []string {
	procs, err := f.Proc.Snapshot()
	if err != nil {
		return []string{"banner: " + err.Error()}
	}
	root, ok := procs[g.RootPID]
	if !ok || (g.StartTime != 0 && root.StartTime != g.StartTime) {
		return nil
	}
	p := bannerProc(procs, root)
	if p == nil {
		return nil
	}
	tty := ttyOf(f.Proc, p.PID)
	if tty == "" {
		return nil
	}
	var warnings []string
	if err := clearBanner(tty, g.Name); err != nil {
		warnings = append(warnings, "banner: "+err.Error())
	}
	if now, err := f.Proc.Read(p.PID); err == nil && now.StartTime == p.StartTime {
		if err := sendWinch(p.PID); err != nil && !errors.Is(err, syscall.ESRCH) {
			warnings = append(warnings, "banner: "+err.Error())
		}
	}
	return warnings
}

// thaw unfreezes the cgroup for key, moves each process in it back to its
// recorded origin (or, if that is unknown or gone, the origin of rootPID),
// removes the cgroup if empty and deletes the state entry once the cgroup is
// gone; while processes remain in it the entry is kept for a later retry. A failure to unfreeze is returned as an error and
// stops there; later problems are returned as warnings. Freeze's rollback
// uses it too.
func (f *Freezer) thaw(key string, rootPID int) (warnings []string, err error) {
	cg := f.CG.Path(key)
	if err := f.CG.SetFrozen(cg, false); err != nil {
		return nil, fmt.Errorf("thaw failed: %w", err)
	}
	if err := f.CG.WaitFrozen(cg, false, f.FreezeTimeout); err != nil {
		return nil, fmt.Errorf("thaw failed: %w", err)
	}

	ents, err := f.Store.Load()
	if err != nil {
		warnings = append(warnings, "state: "+err.Error())
	}
	entry := ents[key]
	rootOrigin := entry.Origins[rootPID]
	pids, err := f.CG.Procs(cg)
	if err != nil {
		warnings = append(warnings, "list "+cg+": "+err.Error())
	}
	usable := func(o string) bool { return o != "" && f.CG.Exists(o) }
	for _, pid := range pids {
		origin := entry.Origins[pid]
		target := origin
		if !usable(target) {
			target = rootOrigin
		}
		if !usable(target) {
			if origin == "" {
				origin = rootOrigin
			}
			if origin == "" {
				warnings = append(warnings, fmt.Sprintf("no origin recorded for pid %d; left in %s", pid, cg))
			} else {
				warnings = append(warnings, fmt.Sprintf("origin %s gone; pid %d left in %s", origin, pid, cg))
			}
			continue
		}
		if err := f.move(target, pid); err != nil && !errors.Is(err, cgroup.ErrGone) {
			warnings = append(warnings, err.Error())
		}
	}

	if err := f.CG.RemoveIfEmpty(cg); err != nil {
		warnings = append(warnings, "remove "+cg+": "+err.Error())
	}
	// Keep the entry (and its origins) while the cgroup still exists, so a
	// later thaw can retry the processes left in it.
	if f.CG.Exists(cg) {
		left, err := f.CG.Procs(cg)
		switch {
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("%s still exists (%v); state entry kept", cg, err))
		case len(left) > 0:
			warnings = append(warnings, fmt.Sprintf("pids %v left in %s; state entry kept", left, cg))
		default:
			warnings = append(warnings, fmt.Sprintf("%s still exists; state entry kept", cg))
		}
		return warnings, nil
	}
	err = f.Store.Update(func(m map[string]state.Entry) error {
		delete(m, key)
		return nil
	})
	if err != nil {
		warnings = append(warnings, "state: "+err.Error())
	}
	return warnings, nil
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
	mi, err := f.Proc.MemInfo()
	if err != nil {
		return Status{}, err
	}
	s.MemAvailable, s.SwapFree = mi.MemAvailable, mi.SwapFree
	return s, nil
}
