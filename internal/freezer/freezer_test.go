//go:build linux

package freezer

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DataMata2/agent-freeze/internal/cgroup"
	"github.com/DataMata2/agent-freeze/internal/discover"
	"github.com/DataMata2/agent-freeze/internal/procfs"
	"github.com/DataMata2/agent-freeze/internal/procfs/procfstest"
	"github.com/DataMata2/agent-freeze/internal/state"
)

const (
	testDelegate = "/user.slice/user-1000.slice/user@1000.service"
	testBase     = testDelegate + "/app.slice/agent-freeze"
	originCg     = testDelegate + "/app.slice/app-term.scope"
	key102       = "claude-102-5000"
	selfPID      = 105
)

var fixedNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// baseProcs is a terminal scope with a shell (101) running claude (102).
// claude has a node child (103) with a grandchild (401) and a bash child
// (104) that runs afz (105, self).
func baseProcs() []procfstest.Fake {
	return []procfstest.Fake{
		{PID: 101, PPID: 1, StartTime: 4000, Comm: "bash", Args: []string{"bash"}, Cgroup: originCg, RSSKB: 4000},
		{PID: 102, PPID: 101, StartTime: 5000, Comm: "claude", Args: []string{"claude"}, Cgroup: originCg, RSSKB: 100000, SwapKB: 10},
		{PID: 103, PPID: 102, StartTime: 5001, Comm: "node", Args: []string{"node", "x.js"}, Cgroup: originCg, RSSKB: 20000, SwapKB: 20},
		{PID: 104, PPID: 102, StartTime: 5002, Comm: "bash", Args: []string{"bash"}, Cgroup: originCg, RSSKB: 3000},
		{PID: 105, PPID: 104, StartTime: 5003, Comm: "afz", Args: []string{"afz", "freeze"}, Cgroup: originCg, RSSKB: 1000},
		{PID: 401, PPID: 103, StartTime: 5004, Comm: "rg", Args: []string{"rg"}, Cgroup: originCg, RSSKB: 5000},
	}
}

// sum102 is the RSS of 102, 103, 104 and 401 in bytes.
const sum102 = (100000 + 20000 + 3000 + 5000) * 1024

type env struct {
	f      *Freezer
	proc   string
	cgRoot string
}

func newEnv(t *testing.T, procs ...procfstest.Fake) *env {
	t.Helper()
	if procs == nil {
		procs = baseProcs()
	}
	e := &env{proc: t.TempDir(), cgRoot: t.TempDir()}
	procfstest.Write(t, e.proc, procs...)
	procfstest.WriteMemInfo(t, e.proc, 16000000, 8000000, 4000000, 3000000)
	if err := os.MkdirAll(e.cgRoot+originCg, 0o755); err != nil {
		t.Fatal(err)
	}
	e.f = &Freezer{
		Proc:          procfs.FS{Root: e.proc},
		CG:            cgroup.Manager{Root: e.cgRoot, Delegate: testDelegate, Base: testBase},
		Store:         state.Store{Path: filepath.Join(t.TempDir(), "state.json")},
		SelfPID:       selfPID,
		FreezeTimeout: 100 * time.Millisecond,
		Now:           func() time.Time { return fixedNow },
	}
	return e
}

func (e *env) dir(cg string) string { return e.cgRoot + cg }

func (e *env) keyDir(key string) string { return e.cgRoot + e.f.CG.Path(key) }

// group lists and returns the group rooted at root.
func (e *env) group(t *testing.T, root int) GroupView {
	t.Helper()
	gs, err := e.f.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gs {
		if g.RootPID == root {
			return g
		}
	}
	t.Fatalf("no group rooted at %d in %+v", root, gs)
	return GroupView{}
}

func put(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func pids(t *testing.T, path string) []int {
	t.Helper()
	var out []int
	for _, f := range strings.Fields(get(t, path)) {
		n, err := strconv.Atoi(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// setRSS rewrites the fake VmRSS of pid.
func setRSS(t *testing.T, proc string, pid int, kb uint64) {
	t.Helper()
	s := fmt.Sprintf("Name:\tx\nVmRSS:\t%8d kB\nVmSwap:\t%8d kB\n", kb, 0)
	put(t, filepath.Join(proc, strconv.Itoa(pid), "status"), s)
}

// setCgroup rewrites the fake /proc/<pid>/cgroup.
func setCgroup(t *testing.T, proc string, pid int, cg string) {
	t.Helper()
	put(t, filepath.Join(proc, strconv.Itoa(pid), "cgroup"), "0::"+cg+"\n")
}

func entries(t *testing.T, s state.Store) map[string]state.Entry {
	t.Helper()
	m, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestFreezeWritesProcsFreezeReclaim(t *testing.T) {
	e := newEnv(t)
	g := e.group(t, 102)
	put(t, e.keyDir(key102)+"/cgroup.events", "populated 1\nfrozen 1\n")

	r := e.f.Freeze(g, true) // 102 is Self: afz (105) runs inside it
	if !r.OK || r.Error != "" || r.Warning != "" {
		t.Fatalf("Freeze = %+v", r)
	}
	if r.ID != "102" || r.Key != key102 || r.Name != g.Name {
		t.Errorf("identity = %q %q %q", r.ID, r.Key, r.Name)
	}
	if got, want := pids(t, e.keyDir(key102)+"/cgroup.procs"), []int{102, 103, 104, 401}; !slices.Equal(got, want) {
		t.Errorf("cgroup.procs = %v, want %v", got, want)
	}
	if got := get(t, e.keyDir(key102)+"/cgroup.freeze"); got != "1" {
		t.Errorf("cgroup.freeze = %q", got)
	}
	wantReclaim := fmt.Sprintf("%d swappiness=200", sum102)
	if got := get(t, e.dir(originCg)+"/memory.reclaim"); !strings.HasPrefix(got, wantReclaim) {
		t.Errorf("origin memory.reclaim = %q, want prefix %q", got, wantReclaim)
	}
	if r.RSSBefore != sum102 || r.RSSAfter != sum102 || r.SwapAfter != 30*1024 {
		t.Errorf("sizes = %d %d %d", r.RSSBefore, r.RSSAfter, r.SwapAfter)
	}
	ent, ok := entries(t, e.f.Store)[key102]
	if !ok {
		t.Fatal("no state entry")
	}
	if len(ent.Origins) != 4 {
		t.Errorf("Origins = %v", ent.Origins)
	}
	for pid, o := range ent.Origins {
		if o != originCg {
			t.Errorf("Origins[%d] = %q", pid, o)
		}
	}
	if ent.Cgroup != testBase+"/"+key102 || ent.Agent != "claude" || ent.Name != g.Name {
		t.Errorf("entry = %+v", ent)
	}
	if !ent.FrozenAt.Equal(fixedNow) || ent.RSSBefore != sum102 || ent.Freed != 0 {
		t.Errorf("entry times/sizes = %+v", ent)
	}
}

func TestFreezeRefusesSelf(t *testing.T) {
	e := newEnv(t)
	g := e.group(t, 102)
	if !g.Self {
		t.Fatal("group 102 should be Self")
	}
	r := e.f.Freeze(g, false)
	if r.OK || r.Error != "refusing to freeze the agent that is running afz (use --force-self)" {
		t.Fatalf("Freeze = %+v", r)
	}
	if exists(e.cgRoot + testBase) {
		t.Error("base cgroup created")
	}
}

func TestFreezeForceSelfExcludesAfz(t *testing.T) {
	e := newEnv(t)
	g := e.group(t, 102)
	put(t, e.keyDir(key102)+"/cgroup.events", "frozen 1\n")
	r := e.f.Freeze(g, true)
	if !r.OK {
		t.Fatalf("Freeze = %+v", r)
	}
	if got := pids(t, e.keyDir(key102)+"/cgroup.procs"); slices.Contains(got, selfPID) {
		t.Errorf("afz (%d) moved: %v", selfPID, got)
	}
	if _, ok := entries(t, e.f.Store)[key102].Origins[selfPID]; ok {
		t.Error("afz recorded in Origins")
	}
}

func TestFreezeAlreadyFrozenIsIdempotent(t *testing.T) {
	e := newEnv(t)
	g := e.group(t, 102)
	g.State = "frozen"
	r := e.f.Freeze(g, true)
	if !r.OK || r.Warning != "already frozen" || r.Error != "" {
		t.Fatalf("Freeze = %+v", r)
	}
	if exists(e.cgRoot+testBase) || exists(e.f.Store.Path) {
		t.Error("files written for an already frozen group")
	}
}

func TestFreezeRootGone(t *testing.T) {
	e := newEnv(t)
	g := e.group(t, 102)
	if err := os.RemoveAll(filepath.Join(e.proc, "102")); err != nil {
		t.Fatal(err)
	}
	r := e.f.Freeze(g, true)
	if r.OK || r.Error != "agent exited" {
		t.Fatalf("Freeze = %+v", r)
	}
	if exists(e.keyDir(key102)) {
		t.Error("cgroup created for an exited agent")
	}
}

func TestFreezeRootPIDReused(t *testing.T) {
	e := newEnv(t)
	g := e.group(t, 102)
	procs := baseProcs()
	procs[1].StartTime = 9999 // 102 is now a different process
	e2 := newEnv(t, procs...)
	e.f.Proc = e2.f.Proc
	r := e.f.Freeze(g, true)
	if r.OK || r.Error != "agent exited" {
		t.Fatalf("Freeze = %+v", r)
	}
	if exists(e.keyDir(key102)) {
		t.Error("cgroup created for a reused PID")
	}
}

func TestFreezeMemberGoneMidMove(t *testing.T) {
	e := newEnv(t)
	g := e.group(t, 102)
	// 103 exits; its child 401 is reparented to init.
	if err := os.RemoveAll(filepath.Join(e.proc, "103")); err != nil {
		t.Fatal(err)
	}
	stat := get(t, filepath.Join(e.proc, "401", "stat"))
	put(t, filepath.Join(e.proc, "401", "stat"), strings.Replace(stat, ") S 103 ", ") S 1 ", 1))
	put(t, e.keyDir(key102)+"/cgroup.events", "frozen 1\n")

	r := e.f.Freeze(g, true)
	if !r.OK {
		t.Fatalf("Freeze = %+v", r)
	}
	got := pids(t, e.keyDir(key102)+"/cgroup.procs")
	if slices.Contains(got, 103) {
		t.Errorf("103 moved: %v", got)
	}
	if !slices.Equal(got, []int{102, 104}) {
		t.Errorf("cgroup.procs = %v, want [102 104]", got)
	}
}

func TestFreezeNotDelegated(t *testing.T) {
	procs := append(baseProcs(),
		procfstest.Fake{PID: 201, PPID: 1, StartTime: 7000, Comm: "claude", Args: []string{"claude"}, Cgroup: "/system.slice/x.service", RSSKB: 1000},
	)
	e := newEnv(t, procs...)
	g := e.group(t, 201)
	if g.Delegated {
		t.Error("Delegated = true for /system.slice group")
	}
	r := e.f.Freeze(g, false)
	if r.OK || !strings.HasPrefix(r.Error, "not in user-delegated cgroup") {
		t.Fatalf("Freeze = %+v", r)
	}
	if r.Error != "not in user-delegated cgroup: /system.slice/x.service" {
		t.Errorf("Error = %q", r.Error)
	}
	if exists(e.cgRoot + testBase) {
		t.Error("base cgroup created")
	}
}

func TestFreezeTimeoutRollsBack(t *testing.T) {
	e := newEnv(t)
	g := e.group(t, 102)
	freezeAtMoveBack := e.recordFreezeOnMoveBack(t, nil)
	put(t, e.keyDir(key102)+"/cgroup.events", "frozen 0\n")

	r := e.f.Freeze(g, true)
	if r.OK || !strings.HasPrefix(r.Error, "freeze failed") {
		t.Fatalf("Freeze = %+v", r)
	}
	if got := *freezeAtMoveBack; got != "0" {
		t.Errorf("cgroup.freeze at move back = %q, want 0", got)
	}
	if got, want := pids(t, e.dir(originCg)+"/cgroup.procs"), []int{102, 103, 104, 401}; !slices.Equal(got, want) {
		t.Errorf("origin cgroup.procs = %v, want %v", got, want)
	}
	if _, ok := entries(t, e.f.Store)[key102]; ok {
		t.Error("state entry kept after rollback")
	}
	if exists(e.keyDir(key102)) {
		t.Error("key cgroup not removed after rollback")
	}
	if exists(e.dir(originCg) + "/memory.reclaim") {
		t.Error("reclaimed after a failed freeze")
	}
}

func TestFreezeMoveErrorRollsBack(t *testing.T) {
	e := newEnv(t)
	g := e.group(t, 102)
	put(t, e.keyDir(key102)+"/cgroup.events", "frozen 0\n")
	// A directory in place of cgroup.procs makes every Move fail.
	if err := os.MkdirAll(e.keyDir(key102)+"/cgroup.procs", 0o755); err != nil {
		t.Fatal(err)
	}
	r := e.f.Freeze(g, true)
	if r.OK || !strings.HasPrefix(r.Error, "freeze failed") {
		t.Fatalf("Freeze = %+v", r)
	}
	if got := get(t, e.keyDir(key102)+"/cgroup.freeze"); got != "0" {
		t.Errorf("cgroup.freeze = %q, want 0", got)
	}
	// The cgroup could not be listed or removed, so the entry and its
	// origins stay for a later thaw.
	if _, ok := entries(t, e.f.Store)[key102]; !ok {
		t.Error("state entry dropped while the key cgroup still exists")
	}
	if !strings.Contains(r.Warning, "state entry kept") {
		t.Errorf("Warning = %q", r.Warning)
	}
}

func TestReclaimFailureIsWarning(t *testing.T) {
	e := newEnv(t)
	g := e.group(t, 102)
	put(t, e.keyDir(key102)+"/cgroup.events", "frozen 1\n")
	if err := os.MkdirAll(e.dir(originCg)+"/memory.reclaim", 0o755); err != nil {
		t.Fatal(err)
	}
	r := e.f.Freeze(g, true)
	if !r.OK || r.Error != "" || !strings.HasPrefix(r.Warning, "reclaim:") {
		t.Fatalf("Freeze = %+v", r)
	}
	if _, ok := entries(t, e.f.Store)[key102]; !ok {
		t.Error("state entry missing")
	}
}

// reclaimEnv freezes group 102 with start RSS kb for 102 (the others keep
// the base values), calling step(round) after each reclaim round. It
// returns the number of rounds run and the Result.
func reclaimRun(t *testing.T, startKB uint64, step func(e *env, round int)) (int, Result) {
	t.Helper()
	procs := baseProcs()
	procs[1].RSSKB = startKB
	e := newEnv(t, procs...)
	g := e.group(t, 102)
	put(t, e.keyDir(key102)+"/cgroup.events", "frozen 1\n")
	rounds := 0
	afterReclaimRound = func(round int) {
		rounds++
		if round != rounds {
			t.Errorf("round %d reported as %d", rounds, round)
		}
		step(e, round)
	}
	t.Cleanup(func() { afterReclaimRound = nil })
	r := e.f.Freeze(g, true)
	if !r.OK {
		t.Fatalf("Freeze = %+v", r)
	}
	return rounds, r
}

func TestReclaimRounds(t *testing.T) {
	if maxReclaimRounds != 4 {
		t.Errorf("maxReclaimRounds = %d", maxReclaimRounds)
	}
	if minRoundProgress != 16<<20 {
		t.Errorf("minRoundProgress = %d", minRoundProgress)
	}
	t.Run("StopsBelowFivePercent", func(t *testing.T) {
		// Start 128000 kB in total; 5% is 6400 kB.
		rounds, r := reclaimRun(t, 100000, func(e *env, round int) {
			switch round {
			case 1:
				setRSS(t, e.proc, 102, 50000) // total 78000, freed 50000 kB
			case 2:
				for _, pid := range []int{102, 103, 104, 401} {
					setRSS(t, e.proc, pid, 1000) // total 4000 (3.1%), freed 74000 kB
				}
			default:
				setRSS(t, e.proc, 102, 0)
			}
		})
		if rounds != 2 {
			t.Errorf("rounds = %d, want 2", rounds)
		}
		if r.RSSAfter != 4000*1024 || r.RSSBefore != sum102 {
			t.Errorf("RSSBefore %d RSSAfter %d", r.RSSBefore, r.RSSAfter)
		}
	})
	t.Run("StopsOnSmallProgress", func(t *testing.T) {
		rounds, r := reclaimRun(t, 100000, func(e *env, round int) {
			switch round {
			case 1:
				setRSS(t, e.proc, 102, 50000) // freed 50000 kB
			case 2:
				setRSS(t, e.proc, 102, 40000) // freed 10000 kB < 16 MiB
			default:
				setRSS(t, e.proc, 102, 0)
			}
		})
		if rounds != 2 {
			t.Errorf("rounds = %d, want 2", rounds)
		}
		if want := uint64(sum102 - 60000*1024); r.RSSAfter != want {
			t.Errorf("RSSAfter = %d, want %d", r.RSSAfter, want)
		}
	})
	t.Run("AtMostFourRounds", func(t *testing.T) {
		kb := uint64(1000000)
		rounds, _ := reclaimRun(t, kb, func(e *env, round int) {
			kb -= 100000 // ~98 MiB per round, never near 5%
			setRSS(t, e.proc, 102, kb)
		})
		if rounds != maxReclaimRounds {
			t.Errorf("rounds = %d, want %d", rounds, maxReclaimRounds)
		}
	})
}

func TestThawMovesBackAndCleansUp(t *testing.T) {
	e := newEnv(t)
	g := e.group(t, 102)
	kd := e.keyDir(key102)
	put(t, kd+"/cgroup.events", "frozen 1\n")
	e.simulateKernel(t, nil)
	if r := e.f.Freeze(g, true); !r.OK {
		t.Fatalf("Freeze = %+v", r)
	}
	// A late child 999 with no recorded origin joined the key cgroup.
	put(t, kd+"/cgroup.procs", "102\n103\n104\n401\n999\n")
	g = e.group(t, 102)
	if g.State != "frozen" {
		t.Fatalf("State = %q, want frozen", g.State)
	}
	if g.FrozenAt == nil || !g.FrozenAt.Equal(fixedNow) {
		t.Errorf("FrozenAt = %v", g.FrozenAt)
	}
	put(t, kd+"/cgroup.events", "frozen 0\n")

	r := e.f.Thaw(g)
	if !r.OK || r.Error != "" || r.Warning != "" {
		t.Fatalf("Thaw = %+v", r)
	}
	if exists(kd) {
		t.Error("key cgroup not removed")
	}
	if got, want := pids(t, e.dir(originCg)+"/cgroup.procs"), []int{102, 103, 104, 401, 999}; !slices.Equal(got, want) {
		t.Errorf("origin cgroup.procs = %v, want %v", got, want)
	}
	if _, ok := entries(t, e.f.Store)[key102]; ok {
		t.Error("state entry not deleted")
	}
	if r.RSSBefore != g.RSS || r.RSSAfter != sum102 || r.SwapAfter != 30*1024 {
		t.Errorf("sizes = %d %d %d", r.RSSBefore, r.RSSAfter, r.SwapAfter)
	}
}

func TestThawNotFrozen(t *testing.T) {
	e := newEnv(t)
	g := e.group(t, 102)
	r := e.f.Thaw(g)
	if !r.OK || r.Warning != "not frozen" || r.Error != "" {
		t.Fatalf("Thaw = %+v", r)
	}
}

// listFixture adds a frozen group (202), a partial group (302), a stale
// state entry and an empty leftover key cgroup to the base fixture.
func listFixture(t *testing.T) *env {
	t.Helper()
	keyB, keyC := "claude-202-6000", "claude-302-7000"
	cgB, cgC := testBase+"/"+keyB, testBase+"/"+keyC
	procs := append(baseProcs(),
		procfstest.Fake{PID: 202, PPID: 1, StartTime: 6000, Comm: "claude", Args: []string{"claude"}, Cgroup: cgB, RSSKB: 2000, SwapKB: 50000},
		procfstest.Fake{PID: 203, PPID: 202, StartTime: 6001, Comm: "node", Args: []string{"node"}, Cgroup: cgB, RSSKB: 1000, SwapKB: 7000},
		procfstest.Fake{PID: 302, PPID: 1, StartTime: 7000, Comm: "claude", Args: []string{"claude"}, Cgroup: cgC, RSSKB: 3000, SwapKB: 30000},
		procfstest.Fake{PID: 303, PPID: 302, StartTime: 7001, Comm: "node", Args: []string{"node"}, Cgroup: originCg, RSSKB: 4000},
	)
	e := newEnv(t, procs...)
	put(t, e.dir(cgB)+"/cgroup.events", "frozen 1\n")
	put(t, e.dir(cgB)+"/cgroup.procs", "202\n203\n")
	put(t, e.dir(cgC)+"/cgroup.events", "frozen 1\n")
	put(t, e.dir(cgC)+"/cgroup.procs", "302\n")
	put(t, e.dir(testBase+"/claude-888-1")+"/cgroup.procs", "")
	old := fixedNow.Add(-10 * time.Minute)
	if err := os.Chtimes(e.dir(testBase+"/claude-888-1"), old, old); err != nil {
		t.Fatal(err)
	}
	err := e.f.Store.Update(func(m map[string]state.Entry) error {
		m[keyB] = state.Entry{Key: keyB, Cgroup: cgB, FrozenAt: fixedNow, Freed: 777}
		m[keyC] = state.Entry{Key: keyC, Cgroup: cgC, FrozenAt: fixedNow, Freed: 55}
		m["claude-999-1"] = state.Entry{Key: "claude-999-1", Cgroup: testBase + "/claude-999-1", Freed: 1}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestListStates(t *testing.T) {
	e := listFixture(t)
	gs, err := e.f.List()
	if err != nil {
		t.Fatal(err)
	}
	byRoot := map[int]GroupView{}
	for _, g := range gs {
		byRoot[g.RootPID] = g
	}
	want := map[int]struct {
		state string
		freed uint64
	}{102: {"running", 0}, 202: {"frozen", 777}, 302: {"partial", 55}}
	for root, w := range want {
		g, ok := byRoot[root]
		if !ok {
			t.Errorf("group %d missing", root)
			continue
		}
		if g.State != w.state || g.Freed != w.freed {
			t.Errorf("group %d: State %q Freed %d, want %q %d", root, g.State, g.Freed, w.state, w.freed)
		}
		if (g.FrozenAt != nil) != (w.state != "running") {
			t.Errorf("group %d: FrozenAt = %v", root, g.FrozenAt)
		}
		if !g.Delegated {
			t.Errorf("group %d not Delegated", root)
		}
	}
	m := entries(t, e.f.Store)
	if _, ok := m["claude-999-1"]; ok {
		t.Error("stale state entry not pruned")
	}
	if len(m) != 2 {
		t.Errorf("entries = %v", m)
	}
	if exists(e.dir(testBase + "/claude-888-1")) {
		t.Error("empty leftover cgroup not removed")
	}
}

func TestStatus(t *testing.T) {
	e := listFixture(t)
	s, err := e.f.Status()
	if err != nil {
		t.Fatal(err)
	}
	want := Status{
		Frozen:       2,
		Freed:        777 + 55,
		FrozenSwap:   (50000 + 7000 + 30000) * 1024,
		MemAvailable: 8000000 * 1024,
		SwapFree:     3000000 * 1024,
	}
	if s != want {
		t.Errorf("Status = %+v, want %+v", s, want)
	}
}

func TestNew(t *testing.T) {
	f := New(1000, 42)
	if f.Proc.Root != "/proc" || f.CG.Base != testBase || f.SelfPID != 42 {
		t.Errorf("New = %+v", f)
	}
	if f.FreezeTimeout != 5*time.Second || f.Now == nil || f.SessionsDir == "" || f.Store.Path == "" {
		t.Errorf("New = %+v", f)
	}
	var _ API = f
}

// simulateKernel makes Moves behave like the kernel for the rest of the
// test: a moved PID leaves every other fake cgroup.procs and its
// /proc/<pid>/cgroup names the new cgroup. extra, if set, runs after each
// move.
func (e *env) simulateKernel(t *testing.T, extra func(cg string, pid int)) {
	t.Helper()
	afterMove = func(cg string, pid int, err error) {
		if err == nil {
			target := e.cgRoot + cg + "/cgroup.procs"
			_ = filepath.WalkDir(e.cgRoot, func(path string, d fs.DirEntry, err error) error {
				if err != nil || path == target || d.Name() != "cgroup.procs" || !d.Type().IsRegular() {
					return nil
				}
				var keep []string
				for _, f := range strings.Fields(get(t, path)) {
					if f != strconv.Itoa(pid) {
						keep = append(keep, f+"\n")
					}
				}
				put(t, path, strings.Join(keep, ""))
				return nil
			})
			if exists(filepath.Join(e.proc, strconv.Itoa(pid))) {
				setCgroup(t, e.proc, pid, cg)
			}
		}
		if extra != nil {
			extra(cg, pid)
		}
	}
	t.Cleanup(func() { afterMove = nil })
}

func TestFreezeRootReplacedBetweenPasses(t *testing.T) {
	e := newEnv(t)
	g := e.group(t, 102)
	put(t, e.keyDir(key102)+"/cgroup.events", "frozen 0\n")
	replaced := false
	freezeAtMoveBack := e.recordFreezeOnMoveBack(t, func(cg string, pid int) {
		if replaced {
			return
		}
		replaced = true
		// 102 exits and its PID is reused by a new process.
		root := baseProcs()[1]
		root.StartTime = 9999
		procfstest.Write(t, e.proc, root)
	})
	r := e.f.Freeze(g, true)
	if r.OK || r.Error != "agent exited" {
		t.Fatalf("Freeze = %+v", r)
	}
	if !replaced {
		t.Fatal("no move happened")
	}
	if got := *freezeAtMoveBack; got != "0" {
		t.Errorf("cgroup.freeze at move back = %q, want 0", got)
	}
	if got, want := pids(t, e.dir(originCg)+"/cgroup.procs"), []int{102, 103, 104, 401}; !slices.Equal(got, want) {
		t.Errorf("origin cgroup.procs = %v, want %v", got, want)
	}
	if exists(e.keyDir(key102)) {
		t.Error("key cgroup not removed after rollback")
	}
	if _, ok := entries(t, e.f.Store)[key102]; ok {
		t.Error("state entry kept after rollback")
	}
}

func TestFreezeNewMemberNotDelegatedRollsBack(t *testing.T) {
	e := newEnv(t)
	g := e.group(t, 102)
	put(t, e.keyDir(key102)+"/cgroup.events", "frozen 0\n")
	spawned := false
	e.simulateKernel(t, func(cg string, pid int) {
		if spawned {
			return
		}
		spawned = true
		procfstest.Write(t, e.proc, procfstest.Fake{PID: 501, PPID: 102, StartTime: 5100, Comm: "sh", Args: []string{"sh"}, Cgroup: "/system.slice/x.service", RSSKB: 10})
	})
	r := e.f.Freeze(g, true)
	if r.OK || r.Error != "not in user-delegated cgroup: /system.slice/x.service" {
		t.Fatalf("Freeze = %+v", r)
	}
	if got, want := pids(t, e.dir(originCg)+"/cgroup.procs"), []int{102, 103, 104, 401}; !slices.Equal(got, want) {
		t.Errorf("origin cgroup.procs = %v, want %v", got, want)
	}
	if exists(e.keyDir(key102)) {
		t.Error("key cgroup not removed after rollback")
	}
	if _, ok := entries(t, e.f.Store)[key102].Origins[501]; ok {
		t.Error("non-delegated PID recorded")
	}
}

func TestFreezeMembersAlreadyInKeyCgroup(t *testing.T) {
	e := newEnv(t)
	cg := testBase + "/" + key102
	for _, pid := range []int{102, 103, 104, 401} {
		setCgroup(t, e.proc, pid, cg)
	}
	put(t, e.keyDir(key102)+"/cgroup.procs", "102\n103\n104\n401\n")
	put(t, e.keyDir(key102)+"/cgroup.events", "frozen 0\n")
	g := e.group(t, 102)
	if g.State != "running" {
		t.Fatalf("State = %q, want running", g.State)
	}
	put(t, e.keyDir(key102)+"/cgroup.events", "frozen 1\n")
	r := e.f.Freeze(g, true)
	if !r.OK {
		t.Fatalf("Freeze = %+v", r)
	}
	ent, ok := entries(t, e.f.Store)[key102]
	if !ok {
		t.Fatal("no state entry")
	}
	if ent.Key != key102 || ent.Name != g.Name || ent.Agent != "claude" || ent.Cgroup != cg {
		t.Errorf("entry half-filled: %+v", ent)
	}
	if !ent.FrozenAt.Equal(fixedNow) || ent.RSSBefore != sum102 {
		t.Errorf("entry = %+v", ent)
	}
	if got, want := pids(t, e.keyDir(key102)+"/cgroup.procs"), []int{102, 103, 104, 401}; !slices.Equal(got, want) {
		t.Errorf("cgroup.procs = %v, want %v (nothing should be moved)", got, want)
	}
}

func TestFreezePartialKeepsOrigins(t *testing.T) {
	e := newEnv(t)
	other := testDelegate + "/app.slice/other.scope"
	cg := testBase + "/" + key102
	for _, pid := range []int{102, 103} {
		setCgroup(t, e.proc, pid, cg)
	}
	put(t, e.keyDir(key102)+"/cgroup.procs", "102\n103\n")
	put(t, e.keyDir(key102)+"/cgroup.events", "frozen 1\n")
	err := e.f.Store.Update(func(m map[string]state.Entry) error {
		m[key102] = state.Entry{Key: key102, Cgroup: cg, FrozenAt: fixedNow.Add(-time.Hour),
			Origins: map[int]string{102: originCg, 103: other}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	g := e.group(t, 102)
	if g.State != "partial" {
		t.Fatalf("State = %q, want partial", g.State)
	}
	r := e.f.Freeze(g, true)
	if !r.OK {
		t.Fatalf("Freeze = %+v", r)
	}
	if got, want := pids(t, e.keyDir(key102)+"/cgroup.procs"), []int{102, 103, 104, 401}; !slices.Equal(got, want) {
		t.Errorf("cgroup.procs = %v, want %v", got, want)
	}
	want := map[int]string{102: originCg, 103: other, 104: originCg, 401: originCg}
	if got := entries(t, e.f.Store)[key102].Origins; !maps.Equal(got, want) {
		t.Errorf("Origins = %v, want %v", got, want)
	}
}

// thawEnv prepares an unfrozen key cgroup holding inKey and, when origins is
// non-nil, a state entry with those origins. Moves are kernel-simulated.
func thawEnv(t *testing.T, origins map[int]string, inKey ...int) (*env, GroupView) {
	t.Helper()
	e := newEnv(t)
	cg := testBase + "/" + key102
	var procs strings.Builder
	for _, pid := range inKey {
		setCgroup(t, e.proc, pid, cg)
		fmt.Fprintf(&procs, "%d\n", pid)
	}
	put(t, e.keyDir(key102)+"/cgroup.procs", procs.String())
	put(t, e.keyDir(key102)+"/cgroup.events", "frozen 0\n")
	if origins != nil {
		err := e.f.Store.Update(func(m map[string]state.Entry) error {
			m[key102] = state.Entry{Key: key102, Cgroup: cg, Origins: origins}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	e.simulateKernel(t, nil)
	g := GroupView{Group: discover.Group{ID: "102", Key: key102, Agent: "claude", RootPID: 102}, State: "frozen"}
	return e, g
}

func TestThawOriginGoneFallsBackToRoot(t *testing.T) {
	gone := testDelegate + "/app.slice/gone.scope"
	e, g := thawEnv(t, map[int]string{102: originCg, 103: gone}, 102, 103)
	r := e.f.Thaw(g)
	if !r.OK || r.Warning != "" {
		t.Fatalf("Thaw = %+v", r)
	}
	if got, want := pids(t, e.dir(originCg)+"/cgroup.procs"), []int{102, 103}; !slices.Equal(got, want) {
		t.Errorf("origin cgroup.procs = %v, want %v", got, want)
	}
	if exists(e.keyDir(key102)) {
		t.Error("key cgroup not removed")
	}
	if _, ok := entries(t, e.f.Store)[key102]; ok {
		t.Error("state entry not deleted")
	}
}

func TestThawNoUsableOriginKeepsEntry(t *testing.T) {
	gone1 := testDelegate + "/app.slice/gone1.scope"
	gone2 := testDelegate + "/app.slice/gone2.scope"
	e, g := thawEnv(t, map[int]string{102: gone1, 103: gone2}, 102, 103)
	cg := testBase + "/" + key102
	r := e.f.Thaw(g)
	if !r.OK {
		t.Fatalf("Thaw = %+v", r)
	}
	for _, want := range []string{
		"origin " + gone1 + " gone; pid 102 left in " + cg,
		"origin " + gone2 + " gone; pid 103 left in " + cg,
		"pids [102 103] left in " + cg + "; state entry kept",
	} {
		if !strings.Contains(r.Warning, want) {
			t.Errorf("Warning %q lacks %q", r.Warning, want)
		}
	}
	if !exists(e.keyDir(key102)) {
		t.Error("non-empty key cgroup removed")
	}
	ent, ok := entries(t, e.f.Store)[key102]
	if !ok || len(ent.Origins) != 2 {
		t.Errorf("state entry not kept: %+v, %v", ent, ok)
	}
}

func TestThawNoOriginRecorded(t *testing.T) {
	e, g := thawEnv(t, nil, 102)
	cg := testBase + "/" + key102
	r := e.f.Thaw(g)
	if !r.OK {
		t.Fatalf("Thaw = %+v", r)
	}
	for _, want := range []string{
		"no origin recorded for pid 102; left in " + cg,
		"pids [102] left in " + cg + "; state entry kept",
	} {
		if !strings.Contains(r.Warning, want) {
			t.Errorf("Warning %q lacks %q", r.Warning, want)
		}
	}
	if !exists(e.keyDir(key102)) {
		t.Error("non-empty key cgroup removed")
	}
}

// recordFreezeOnMoveBack simulates the kernel and records the key cgroup's
// cgroup.freeze at the first move back to the origin scope (the key cgroup
// is removed afterwards, so it can't be read at the end).
func (e *env) recordFreezeOnMoveBack(t *testing.T, extra func(cg string, pid int)) *string {
	t.Helper()
	got := new(string)
	*got = "<no move back>"
	recorded := false
	e.simulateKernel(t, func(cg string, pid int) {
		if cg == originCg && !recorded {
			recorded = true
			*got = get(t, e.keyDir(key102)+"/cgroup.freeze")
		}
		if extra != nil {
			extra(cg, pid)
		}
	})
	return got
}

// emptyKeyDir creates an empty fake key cgroup whose directory is age old
// relative to fixedNow.
func emptyKeyDir(t *testing.T, e *env, key string, age time.Duration) string {
	t.Helper()
	dir := e.keyDir(key)
	put(t, dir+"/cgroup.procs", "")
	at := fixedNow.Add(-age)
	if err := os.Chtimes(dir, at, at); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestHousekeepKeepsYoungEmptyCgroup(t *testing.T) {
	e := newEnv(t)
	dir := emptyKeyDir(t, e, "claude-888-1", 5*time.Second)
	if _, err := e.f.List(); err != nil {
		t.Fatal(err)
	}
	if !exists(dir) {
		t.Error("young empty key cgroup removed")
	}
}

func TestHousekeepKeepsInProgressFreeze(t *testing.T) {
	e := newEnv(t)
	key := "claude-888-1"
	dir := emptyKeyDir(t, e, key, 5*time.Minute)
	err := e.f.Store.Update(func(m map[string]state.Entry) error {
		m[key] = state.Entry{Key: key, Cgroup: e.f.CG.Path(key)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.f.List(); err != nil {
		t.Fatal(err)
	}
	if !exists(dir) {
		t.Error("key cgroup of an in-progress freeze removed")
	}
	if _, ok := entries(t, e.f.Store)[key]; !ok {
		t.Error("in-progress state entry pruned")
	}
}

func TestHousekeepRemovesOldEmptyCgroup(t *testing.T) {
	e := newEnv(t)
	dir := emptyKeyDir(t, e, "claude-888-1", 10*time.Minute)
	if _, err := e.f.List(); err != nil {
		t.Fatal(err)
	}
	if exists(dir) {
		t.Error("old empty key cgroup not removed")
	}
}

func TestHousekeepRemovesStaleInProgressCgroup(t *testing.T) {
	e := newEnv(t)
	key := "claude-888-1"
	dir := emptyKeyDir(t, e, key, 20*time.Minute)
	err := e.f.Store.Update(func(m map[string]state.Entry) error {
		m[key] = state.Entry{Key: key, Cgroup: e.f.CG.Path(key)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.f.List(); err != nil {
		t.Fatal(err)
	}
	if exists(dir) {
		t.Error("stale in-progress key cgroup not removed")
	}
	if _, ok := entries(t, e.f.Store)[key]; ok {
		t.Error("stale in-progress state entry not pruned")
	}
}
