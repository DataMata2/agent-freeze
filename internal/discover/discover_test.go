//go:build linux

package discover_test

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/DataMata2/agent-freeze/internal/discover"
	"github.com/DataMata2/agent-freeze/internal/procfs"
	"github.com/DataMata2/agent-freeze/internal/procfs/procfstest"
	"github.com/DataMata2/agent-freeze/internal/sessions"
)

const mb = 1024 // kB per MB, for RSSKB values

const (
	termScope   = "/user.slice/user-1000.slice/user@1000.service/app.slice/alacritty.scope"
	daemonScope = "/user.slice/user-1000.slice/user@1000.service/app.slice/claude-daemon.service"
	codexScope  = "/user.slice/user-1000.slice/user@1000.service/app.slice/codex.scope"
)

// machineFakes is the fixture tree that mirrors the real machine.
func machineFakes() []procfstest.Fake {
	return []procfstest.Fake{
		{PID: 1, PPID: 0, StartTime: 1, Comm: "systemd", Exe: "/usr/lib/systemd/systemd", Cwd: "/",
			Cgroup: "/init.scope", Args: []string{"/sbin/init"}, RSSKB: 12 * mb},
		{PID: 100, PPID: 1, StartTime: 1000, Comm: "alacritty", Exe: "/usr/bin/alacritty", Cwd: "/home/u",
			Cgroup: termScope, Args: []string{"alacritty"}, RSSKB: 60 * mb},
		{PID: 101, PPID: 100, StartTime: 1010, Comm: "bash", Exe: "/usr/bin/bash", Cwd: "/home/u/acme",
			Cgroup: termScope, Args: []string{"-bash"}, RSSKB: 5 * mb},
		{PID: 102, PPID: 101, StartTime: 1020, Comm: "claude", Exe: "/x/claude", Cwd: "/home/u/acme",
			Cgroup: termScope, Args: []string{"claude", "-n", "Acme web frontend builder", "long prompt"},
			RSSKB: 400 * mb, SwapKB: 10 * mb},
		{PID: 103, PPID: 102, StartTime: 1030, Comm: "node", Exe: "/usr/bin/node", Cwd: "/home/u/acme",
			Cgroup: termScope, Args: []string{"node", "/x/obsidian-mcp-server"}, RSSKB: 80 * mb},
		{PID: 104, PPID: 102, StartTime: 1040, Comm: "bash", Exe: "/usr/bin/bash", Cwd: "/home/u/acme",
			Cgroup: termScope, Args: []string{"/bin/bash", "-c", "afz list"}, RSSKB: 4 * mb},
		{PID: 105, PPID: 104, StartTime: 1050, Comm: "afz", Exe: "/home/u/go/bin/afz", Cwd: "/home/u/acme",
			Cgroup: termScope, Args: []string{"afz", "list"}, RSSKB: 8 * mb},
		{PID: 200, PPID: 1, StartTime: 2000, Comm: "claude", Exe: "/x/claude", Cwd: "/home/u",
			Cgroup: daemonScope, Args: []string{"claude", "daemon", "run", "--origin", "transient"}, RSSKB: 80 * mb},
		{PID: 201, PPID: 200, StartTime: 2010, Comm: "claude", Exe: "/x/claude", Cwd: "/home/u",
			Cgroup: daemonScope, Args: []string{"claude", "bg-pty-host", "--bg-pty-host", "/tmp/s.sock", "206", "46",
				"--", "/x/claude", "--session-id", "f0a5", "--resume", "/p/593c.jsonl", "--model", "opus"},
			RSSKB: 20 * mb},
		{PID: 202, PPID: 201, StartTime: 2020, Comm: "claude", Exe: "/x/claude", Cwd: "/home/u/back",
			Cgroup: daemonScope, Args: []string{"/x/claude", "--session-id", "f0a5", "--resume", "/p/593c.jsonl", "--model", "opus"},
			RSSKB: 190 * mb, SwapKB: 3 * mb},
		{PID: 210, PPID: 200, StartTime: 2100, Comm: "claude", Exe: "/x/claude", Cwd: "/home/u",
			Cgroup: daemonScope, Args: []string{"claude", "bg-pty-host", "--bg-pty-host", "/tmp/t.sock", "200", "50",
				"--", "/x/claude", "--bg-spare", "/tmp/c.sock"},
			RSSKB: 20 * mb},
		{PID: 211, PPID: 210, StartTime: 2110, Comm: "claude", Exe: "/x/claude", Cwd: "/home/u",
			Cgroup: daemonScope, Args: []string{"/x/claude", "--bg-spare", "/tmp/c.sock"}, RSSKB: 150 * mb},
		{PID: 300, PPID: 1, StartTime: 3000, Comm: "codex", Exe: "/x/codex", Cwd: "/home/u",
			Cgroup: codexScope, Args: []string{"/x/codex", "app-server", "--listen", "unix://"}, RSSKB: 50 * mb},
		{PID: 400, PPID: 101, StartTime: 4000, Comm: "node", Exe: "/usr/bin/node", Cwd: "/home/u/g",
			Cgroup: termScope, Args: []string{"node", "/usr/lib/node_modules/@google/gemini-cli/dist/gemini.js"},
			RSSKB: 120 * mb},
		{PID: 401, PPID: 102, StartTime: 4010, Comm: "claude", Exe: "/x/claude", Cwd: "/home/u/acme",
			Cgroup: termScope, Args: []string{"claude", "-p", "sub task"}, RSSKB: 100 * mb},
	}
}

// machine writes the fixture tree (plus any extra fakes) and snapshots it.
func machine(t *testing.T, extra ...procfstest.Fake) map[int]*procfs.Process {
	t.Helper()
	root := t.TempDir()
	procfstest.Write(t, root, append(machineFakes(), extra...)...)
	procs, err := procfs.FS{Root: root}.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return procs
}

func byRoot(t *testing.T, groups []discover.Group) map[int]discover.Group {
	t.Helper()
	m := make(map[int]discover.Group, len(groups))
	for _, g := range groups {
		if _, dup := m[g.RootPID]; dup {
			t.Fatalf("duplicate group for root %d", g.RootPID)
		}
		m[g.RootPID] = g
	}
	return m
}

func TestAgentOf(t *testing.T) {
	procs := machine(t)
	for pid, want := range map[int]string{
		102: "claude", 103: "", 400: "gemini", 300: "codex",
		1: "", 101: "", 105: "", 202: "claude",
	} {
		if got := discover.AgentOf(procs[pid]); got != want {
			t.Errorf("AgentOf(%d) = %q, want %q", pid, got, want)
		}
	}
}

func TestAgentOfInterpreters(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"node", "--max-old-space-size=4096", "/usr/lib/node_modules/@openai/codex/bin/codex.js"}, "codex"},
		{[]string{"node", "/home/u/.npm/node_modules/opencode/dist/index.mjs"}, "opencode"},
		{[]string{"node", "/opt/node_modules/@sourcegraph/amp/dist/main.cjs"}, "amp"},
		{[]string{"python3", "-m", "/usr/bin/aider.py"}, "aider"},
		{[]string{"bun", "run", "/x/claude.ts"}, ""}, // first non-flag arg is "run"
		{[]string{"node", "/x/server.js"}, ""},
		{[]string{"ruby", "/x/claude.rb"}, ""},
	}
	for _, c := range cases {
		p := &procfs.Process{PID: 9, Comm: c.args[0], Exe: "/usr/bin/" + c.args[0], Args: c.args}
		if got := discover.AgentOf(p); got != c.want {
			t.Errorf("AgentOf(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestGroupsRoots(t *testing.T) {
	procs := machine(t)
	gs := discover.Groups(procs, 999)
	var roots []int
	for _, g := range gs {
		roots = append(roots, g.RootPID)
	}
	slices.Sort(roots)
	if want := []int{102, 200, 201, 210, 300, 400}; !reflect.DeepEqual(roots, want) {
		t.Fatalf("roots = %v, want %v", roots, want)
	}
	m := byRoot(t, gs)
	wantPIDs := map[int][]int{
		102: {102, 103, 104, 105, 401},
		200: {200},
		201: {201, 202},
		210: {210, 211},
		300: {300},
		400: {400},
	}
	for root, want := range wantPIDs {
		if got := m[root].PIDs; !reflect.DeepEqual(got, want) {
			t.Errorf("group %d PIDs = %v, want %v", root, got, want)
		}
	}
	if got := discover.Members(procs, 200, 999); slices.Contains(got, 201) || slices.Contains(got, 202) {
		t.Errorf("Members(200) = %v, must not include 201's subtree", got)
	}
	if got := discover.Members(procs, 4242, 999); got != nil {
		t.Errorf("Members(missing) = %v, want nil", got)
	}
}

func TestGroupsNamesAndFlags(t *testing.T) {
	m := byRoot(t, discover.Groups(machine(t), 999))
	type want struct {
		agent, name, session, cwd string
		daemon                    bool
	}
	for root, w := range map[int]want{
		102: {"claude", "Acme web frontend builder", "", "/home/u/acme", false},
		201: {"claude", "f0a5", "f0a5", "/home/u/back", false},
		200: {"claude", "claude daemon", "", "/home/u", true},
		210: {"claude", "claude bg-spare", "", "/home/u", true},
		300: {"codex", "codex app-server", "", "/home/u", true},
		400: {"gemini", "gemini @ g", "", "/home/u/g", false},
	} {
		g := m[root]
		if g.Agent != w.agent || g.Name != w.name || g.SessionID != w.session || g.Cwd != w.cwd || g.Daemon != w.daemon {
			t.Errorf("group %d = {agent %q name %q session %q cwd %q daemon %v}, want %+v",
				root, g.Agent, g.Name, g.SessionID, g.Cwd, g.Daemon, w)
		}
		if g.ID != fmt.Sprint(root) {
			t.Errorf("group %d ID = %q", root, g.ID)
		}
	}
}

func TestSessionAndNameParsing(t *testing.T) {
	cases := []struct {
		args          []string
		name, session string
	}{
		{[]string{"claude", "--resume=/p/abc.jsonl"}, "abc", "abc"},
		{[]string{"claude", "--resume", "r1", "--session-id=s1"}, "s1", "s1"},
		{[]string{"claude", "--name=  spaced \t  out  ", "--resume", "r1"}, "spaced out", "r1"},
		{[]string{"claude", "-n=short"}, "short", ""},
		{[]string{"claude", "--name", "a\nb"}, "a b", ""},
		// A bare flag followed by another flag has no value.
		{[]string{"claude", "--resume", "--model", "opus"}, "claude @ proj", ""},
		{[]string{"claude", "-n", "--model", "opus"}, "claude @ proj", ""},
		{[]string{"claude", "--resume", "--session-id", "s2"}, "s2", "s2"},
		{[]string{"claude", "-n", "--resume", "r3"}, "r3", "r3"},
	}
	for i, c := range cases {
		root := t.TempDir()
		procfstest.Write(t, root, procfstest.Fake{PID: 50, PPID: 1, StartTime: 5, Comm: "claude",
			Exe: "/x/claude", Cwd: "/home/u/proj", Args: c.args})
		procs, err := procfs.FS{Root: root}.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		gs := discover.Groups(procs, 999)
		if len(gs) != 1 {
			t.Fatalf("case %d: %d groups", i, len(gs))
		}
		if gs[0].Name != c.name || gs[0].SessionID != c.session {
			t.Errorf("case %d %q: name %q session %q, want %q %q", i, c.args, gs[0].Name, gs[0].SessionID, c.name, c.session)
		}
	}
}

func TestGroupsSelf(t *testing.T) {
	procs := machine(t)
	found := false
	for _, g := range discover.Groups(procs, 105) {
		if g.RootPID == 102 {
			found = true
			if !g.Self {
				t.Errorf("group 102 Self = false, want true")
			}
			if slices.Contains(g.PIDs, 105) {
				t.Errorf("group 102 PIDs %v contain self 105", g.PIDs)
			}
			if want := []int{102, 103, 104, 401}; !reflect.DeepEqual(g.PIDs, want) {
				t.Errorf("group 102 PIDs = %v, want %v", g.PIDs, want)
			}
		} else if g.Self {
			t.Errorf("group %d Self = true, want false", g.RootPID)
		}
	}
	if !found {
		t.Fatalf("Groups(105): no group 102")
	}
	for _, g := range discover.Groups(procs, 999) {
		if g.Self {
			t.Errorf("Groups(999): group %d Self = true", g.RootPID)
		}
	}
	if got := discover.Members(procs, 102, 105); slices.Contains(got, 105) {
		t.Errorf("Members(102, self 105) = %v", got)
	}
}

// TestGroupsSelfUnderDaemonSession pins the spec rule "any group containing
// one of afz's ancestry PIDs is self": afz under a daemon-hosted session
// marks both the session (201) and the daemon (200) groups.
func TestGroupsSelfUnderDaemonSession(t *testing.T) {
	procs := machine(t, procfstest.Fake{PID: 230, PPID: 202, StartTime: 2300, Comm: "afz",
		Exe: "/home/u/go/bin/afz", Cwd: "/home/u/back", Cgroup: daemonScope,
		Args: []string{"afz", "list"}, RSSKB: 8 * mb})
	gs := discover.Groups(procs, 230)
	m := byRoot(t, gs)
	for root, want := range map[int]bool{200: true, 201: true, 102: false, 210: false, 300: false, 400: false} {
		g, ok := m[root]
		if !ok {
			t.Fatalf("no group %d", root)
		}
		if g.Self != want {
			t.Errorf("group %d Self = %v, want %v", root, g.Self, want)
		}
	}
	for _, g := range gs {
		if slices.Contains(g.PIDs, 230) {
			t.Errorf("group %d PIDs %v contain self 230", g.RootPID, g.PIDs)
		}
	}
	if got, want := m[201].PIDs, []int{201, 202}; !reflect.DeepEqual(got, want) {
		t.Errorf("group 201 PIDs = %v, want %v", got, want)
	}
}

func TestKeyAndSort(t *testing.T) {
	gs := discover.Groups(machine(t), 999)
	m := byRoot(t, gs)
	if got := m[102].Key; got != "claude-102-1020" {
		t.Errorf("Key = %q, want claude-102-1020", got)
	}
	if m[102].StartTime != 1020 {
		t.Errorf("StartTime = %d", m[102].StartTime)
	}
	wantRSS := uint64((400 + 80 + 4 + 8 + 100) * mb * 1024)
	if m[102].RSS != wantRSS || m[102].Swap != 10*mb*1024 {
		t.Errorf("group 102 RSS/Swap = %d/%d, want %d/%d", m[102].RSS, m[102].Swap, wantRSS, 10*mb*1024)
	}
	for i := 1; i < len(gs); i++ {
		a, b := gs[i-1], gs[i]
		if a.RSS < b.RSS || (a.RSS == b.RSS && a.RootPID > b.RootPID) {
			t.Errorf("not sorted at %d: %d(%d) before %d(%d)", i, a.RootPID, a.RSS, b.RootPID, b.RSS)
		}
	}
	if len(gs) == 0 {
		t.Fatal("no groups")
	}
	if gs[0].RootPID != 102 {
		t.Errorf("first group = %d, want 102", gs[0].RootPID)
	}
}

func TestSelect(t *testing.T) {
	procs := machine(t, procfstest.Fake{PID: 500, PPID: 1, StartTime: 5000, Comm: "claude",
		Exe: "/x/claude", Cwd: "/home/u/build", Cgroup: termScope,
		Args: []string{"claude", "-n", "build 102 helper"}, RSSKB: 30 * mb})
	gs := discover.Groups(procs, 999)

	ids := func(gs []discover.Group) []int {
		var out []int
		for _, g := range gs {
			out = append(out, g.RootPID)
		}
		slices.Sort(out)
		return out
	}
	if got := ids(discover.Select(gs, "102")); !reflect.DeepEqual(got, []int{102}) {
		t.Errorf(`Select "102" = %v, want [102]`, got)
	}
	if got := ids(discover.Select(gs, "claude-102-1020")); !reflect.DeepEqual(got, []int{102}) {
		t.Errorf(`Select key = %v, want [102]`, got)
	}
	if got := ids(discover.Select(gs, "acme")); !reflect.DeepEqual(got, []int{102}) {
		t.Errorf(`Select "acme" = %v, want [102]`, got)
	}
	if got := ids(discover.Select(gs, "helper")); !reflect.DeepEqual(got, []int{500}) {
		t.Errorf(`Select "helper" = %v, want [500]`, got)
	}
	if got := discover.Select(gs, "claude"); len(got) < 2 {
		t.Errorf(`Select "claude" = %v, want several`, ids(got))
	}
	if got := discover.Select(gs, ""); got != nil {
		t.Errorf(`Select "" = %v, want nil`, got)
	}
}

func TestCycleAndMissingParent(t *testing.T) {
	root := t.TempDir()
	procfstest.Write(t, root,
		// 600 <-> 601 form a PPID loop.
		procfstest.Fake{PID: 600, PPID: 601, StartTime: 6, Comm: "claude", Exe: "/x/claude", Cwd: "/w/a",
			Args: []string{"claude"}, RSSKB: 10},
		procfstest.Fake{PID: 601, PPID: 600, StartTime: 7, Comm: "bash", Exe: "/usr/bin/bash", Args: []string{"bash"}, RSSKB: 1},
		// 700's parent does not exist.
		procfstest.Fake{PID: 700, PPID: 9999, StartTime: 8, Comm: "codex", Exe: "/x/codex", Cwd: "/w/b",
			Args: []string{"codex"}, RSSKB: 20},
		// 800 -> 801 -> 802 -> 801: loop of non-agents above an agent.
		procfstest.Fake{PID: 800, PPID: 801, StartTime: 9, Comm: "aider", Exe: "/x/aider", Cwd: "/w/c",
			Args: []string{"aider"}, RSSKB: 30},
		procfstest.Fake{PID: 801, PPID: 802, StartTime: 10, Comm: "sh", Exe: "/bin/sh", Args: []string{"sh"}},
		procfstest.Fake{PID: 802, PPID: 801, StartTime: 11, Comm: "sh", Exe: "/bin/sh", Args: []string{"sh"}},
	)
	procs, err := procfs.FS{Root: root}.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, self := range []int{999, 601, 802} {
		m := byRoot(t, discover.Groups(procs, self))
		var roots []int
		for r := range m {
			roots = append(roots, r)
		}
		slices.Sort(roots)
		if want := []int{600, 700, 800}; !reflect.DeepEqual(roots, want) {
			t.Fatalf("self %d: roots = %v, want %v", self, roots, want)
		}
		if self == 999 {
			if got, want := m[600].PIDs, []int{600, 601}; !reflect.DeepEqual(got, want) {
				t.Errorf("group 600 PIDs = %v, want %v", got, want)
			}
		}
	}
}

func TestChromeNativeHostIsDaemon(t *testing.T) {
	root := t.TempDir()
	procfstest.Write(t, root, procfstest.Fake{PID: 50, PPID: 1, StartTime: 5, Comm: "claude",
		Exe: "/x/claude", Cwd: "/home/u", Args: []string{"/x/claude", "--chrome-native-host"}})
	procs, err := procfs.FS{Root: root}.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	gs := discover.Groups(procs, 999)
	if len(gs) != 1 {
		t.Fatalf("%d groups, want 1", len(gs))
	}
	if !gs[0].Daemon || gs[0].Name != "claude chrome-native-host" {
		t.Errorf("group = {daemon %v name %q}, want {true \"claude chrome-native-host\"}", gs[0].Daemon, gs[0].Name)
	}
}

// enrichOne snapshots the machine plus a claude at PID 500 (StartTime 5000,
// cwd /home/u/x) with args, enriches with infos and returns group 500.
func enrichOne(t *testing.T, args []string, infos map[int]sessions.Info) discover.Group {
	t.Helper()
	procs := machine(t, procfstest.Fake{PID: 500, PPID: 1, StartTime: 5000, Comm: "claude",
		Exe: "/x/claude", Cwd: "/home/u/x", Cgroup: termScope, Args: args, RSSKB: 30 * mb})
	gs := discover.Groups(procs, 999)
	discover.Enrich(gs, procs, infos)
	return byRoot(t, gs)[500]
}

func TestEnrichUserNameWins(t *testing.T) {
	g := enrichOne(t, []string{"claude", "-n", "old"}, map[int]sessions.Info{
		500: {PID: 500, SessionID: "sid", ProcStart: "5000", Name: "afz  overseer", NameSource: "user", Status: "busy"},
	})
	if g.Name != "afz overseer" || g.SessionID != "sid" || g.Status != "busy" {
		t.Errorf("group = {name %q session %q status %q}, want {afz overseer sid busy}", g.Name, g.SessionID, g.Status)
	}
}

func TestEnrichArgvBeatsDerived(t *testing.T) {
	g := enrichOne(t, []string{"claude", "-n", "old"}, map[int]sessions.Info{
		500: {PID: 500, SessionID: "sid", ProcStart: "5000", Name: "derived-name", NameSource: "derived", Status: "idle"},
	})
	if g.Name != "old" || g.SessionID != "sid" || g.Status != "idle" {
		t.Errorf("group = {name %q session %q status %q}, want {old sid idle}", g.Name, g.SessionID, g.Status)
	}
}

func TestEnrichDerivedBeatsFallback(t *testing.T) {
	g := enrichOne(t, []string{"claude"}, map[int]sessions.Info{
		500: {PID: 500, SessionID: "sid", Name: "acme-api-e1", NameSource: "derived", Status: "shell"},
	})
	if g.Name != "acme-api-e1" || g.SessionID != "sid" || g.Status != "shell" {
		t.Errorf("group = {name %q session %q status %q}, want {acme-api-e1 sid shell}", g.Name, g.SessionID, g.Status)
	}
	// An empty name never overrides, and an empty session ID keeps the argv one.
	g = enrichOne(t, []string{"claude", "--session-id", "argv-sid"}, map[int]sessions.Info{
		500: {PID: 500, ProcStart: "5000", Name: " ", NameSource: "user", Status: "idle"},
	})
	if g.Name != "argv-sid" || g.SessionID != "argv-sid" || g.Status != "idle" {
		t.Errorf("empty info: group = {name %q session %q status %q}, want {argv-sid argv-sid idle}", g.Name, g.SessionID, g.Status)
	}
	g = enrichOne(t, []string{"claude"}, map[int]sessions.Info{
		500: {PID: 500, NameSource: "derived", Status: "busy"},
	})
	if g.Name != "claude @ x" || g.Status != "busy" {
		t.Errorf("empty derived: group = {name %q status %q}, want {claude @ x busy}", g.Name, g.Status)
	}
}

func TestEnrichStaleProcStart(t *testing.T) {
	procs := machine(t, procfstest.Fake{PID: 500, PPID: 1, StartTime: 5000, Comm: "claude",
		Exe: "/x/claude", Cwd: "/home/u/x", Cgroup: termScope, Args: []string{"claude"}, RSSKB: 30 * mb})
	gs := discover.Groups(procs, 999)
	before := slices.Clone(gs)
	discover.Enrich(gs, procs, map[int]sessions.Info{
		500: {PID: 500, SessionID: "sid", ProcStart: "4999", Name: "reused", NameSource: "user", Status: "busy"},
	})
	if !reflect.DeepEqual(gs, before) {
		t.Errorf("stale procStart changed groups:\n got %+v\nwant %+v", gs, before)
	}
}

func TestEnrichPtyHostUsesChild(t *testing.T) {
	procs := machine(t)
	gs := discover.Groups(procs, 999)
	discover.Enrich(gs, procs, map[int]sessions.Info{
		201: {PID: 201, SessionID: "host", Name: "wrong", NameSource: "user", Status: "shell"},
		202: {PID: 202, SessionID: "child-sid", ProcStart: "2020", Name: "back session", NameSource: "user", Status: "busy"},
	})
	g := byRoot(t, gs)[201]
	if g.Name != "back session" || g.SessionID != "child-sid" || g.Status != "busy" {
		t.Errorf("group 201 = {name %q session %q status %q}, want {back session child-sid busy}", g.Name, g.SessionID, g.Status)
	}
}

func TestEnrichPtyHostWithoutClaudeChild(t *testing.T) {
	procs := machine(t, procfstest.Fake{PID: 600, PPID: 200, StartTime: 6000, Comm: "claude", Exe: "/x/claude",
		Cwd: "/home/u", Cgroup: daemonScope, Args: []string{"claude", "bg-pty-host", "--bg-pty-host", "/tmp/u.sock",
			"--", "/x/claude"}, RSSKB: 20 * mb})
	gs := discover.Groups(procs, 999)
	before := slices.Clone(gs)
	discover.Enrich(gs, procs, map[int]sessions.Info{
		600: {PID: 600, SessionID: "host", Name: "wrong", NameSource: "user", Status: "busy"},
	})
	if !reflect.DeepEqual(gs, before) {
		t.Errorf("childless pty host changed groups:\n got %+v\nwant %+v", gs, before)
	}
}

func TestEnrichSkipsDaemons(t *testing.T) {
	procs := machine(t)
	gs := discover.Groups(procs, 999)
	before := slices.Clone(gs)
	info := func(pid int) sessions.Info {
		return sessions.Info{PID: pid, SessionID: "x", Name: "x", NameSource: "user", Status: "busy"}
	}
	infos := map[int]sessions.Info{}
	for _, pid := range []int{200, 210, 211, 300, 400} {
		infos[pid] = info(pid)
	}
	discover.Enrich(gs, procs, infos)
	if !reflect.DeepEqual(gs, before) {
		t.Errorf("Enrich changed daemon or non-claude groups:\n got %+v\nwant %+v", gs, before)
	}
}

func TestEnrichMissingProc(t *testing.T) {
	gs := []discover.Group{{ID: "77", Agent: "claude", Name: "claude", RootPID: 77}}
	discover.Enrich(gs, map[int]*procfs.Process{}, map[int]sessions.Info{
		77: {PID: 77, Name: "n", NameSource: "user", Status: "busy"},
	})
	if gs[0].Name != "claude" || gs[0].Status != "" {
		t.Errorf("group = %+v, want unchanged", gs[0])
	}
}
