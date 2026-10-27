package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataMata2/agent-freeze/internal/discover"
	"github.com/DataMata2/agent-freeze/internal/freezer"
)

var update = flag.Bool("update", false, "rewrite golden files")

const mib = 1 << 20

type freezeCall struct {
	id        string
	forceSelf bool
}

type fakeAPI struct {
	groups       []freezer.GroupView
	status       freezer.Status
	listErr      error
	statusErr    error
	froze        []string
	freezeCalls  []freezeCall
	thawed       []string
	freezeResult func(g freezer.GroupView, forceSelf bool) freezer.Result
}

func (f *fakeAPI) List() ([]freezer.GroupView, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return slices.Clone(f.groups), nil
}

func (f *fakeAPI) Freeze(g freezer.GroupView, forceSelf bool) freezer.Result {
	f.freezeCalls = append(f.freezeCalls, freezeCall{g.ID, forceSelf})
	if f.freezeResult != nil {
		r := f.freezeResult(g, forceSelf)
		if r.OK {
			f.froze = append(f.froze, g.ID)
		}
		return r
	}
	if g.Self && !forceSelf {
		return freezer.Result{ID: g.ID, Key: g.Key, Name: g.Name, Error: "refusing to freeze the group afz runs in; use --force-self"}
	}
	f.froze = append(f.froze, g.ID)
	return freezer.Result{ID: g.ID, Key: g.Key, Name: g.Name, OK: true, RSSBefore: g.RSS, RSSAfter: g.RSS / 4, SwapAfter: g.RSS - g.RSS/4}
}

func (f *fakeAPI) Thaw(g freezer.GroupView) freezer.Result {
	f.thawed = append(f.thawed, g.ID)
	return freezer.Result{ID: g.ID, Key: g.Key, Name: g.Name, OK: true, RSSBefore: g.RSS, RSSAfter: g.RSS}
}

func (f *fakeAPI) Status() (freezer.Status, error) {
	if f.statusErr != nil {
		return freezer.Status{}, f.statusErr
	}
	return f.status, nil
}

func gv(id, agent, name, state, status, cwd string, rss, swap uint64, pids []int) freezer.GroupView {
	return freezer.GroupView{
		Group: discover.Group{
			ID: id, Key: agent + "-" + id + "-1000", Agent: agent, Name: name, Cwd: cwd,
			RootPID: atoi(id), StartTime: 1000, PIDs: pids, RSS: rss, Swap: swap, Status: status,
		},
		State: state,
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func newFake(t *testing.T) *fakeAPI {
	t.Helper()
	t.Setenv("HOME", "/home/u")
	frozenAt := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	web := gv("102", "claude", "Acme web frontend builder", "running", "busy", "/home/u/code/acme", 400*mib, 0, []int{102, 110, 111})
	back := gv("201", "claude", "claude back", "frozen", "idle", "/srv/back", 30*mib, 250*mib, []int{201})
	back.FrozenAt = &frozenAt
	back.Freed = 250 * mib
	back.Delegated = true
	daemon := gv("200", "claude", "claude daemon", "running", "", "/home/u", 50*mib, 0, []int{200, 205})
	daemon.Daemon = true
	me := gv("300", "claude", "me", "running", "shell", "/home/u/Projects/afz", 120*mib, 0, []int{300, 301, 302, 303})
	me.Self = true
	return &fakeAPI{
		groups: []freezer.GroupView{web, back, daemon, me},
		status: freezer.Status{Frozen: 1, Freed: 250 * mib, FrozenSwap: 250 * mib, MemAvailable: 8 << 30, SwapFree: 16 << 30},
	}
}

func run(api freezer.API, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = Run(args, api, &out, &errb)
	return code, out.String(), errb.String()
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// listFake adds a long-named partial group to exercise truncation.
func listFake(t *testing.T) *fakeAPI {
	f := newFake(t)
	long := gv("410", "codex", "a very long session name that goes past forty runes", "partial", "idle", "/tmp/x", 3*mib/2, 2<<30, []int{410})
	f.groups = append(f.groups, long)
	return f
}

func TestListText(t *testing.T) {
	code, out, errs := run(listFake(t), "list")
	if code != 0 || errs != "" {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	golden(t, "list", out)
}

func TestListAllProcs(t *testing.T) {
	code, out, _ := run(listFake(t), "list", "--all-procs")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	golden(t, "list_all_procs", out)
	if !strings.Contains(out, "\n  pids: 102 110 111\n") {
		t.Errorf("missing pids line:\n%s", out)
	}
	if got := strings.Count(out, "  pids: "); got != 5 {
		t.Errorf("pids lines = %d, want 5", got)
	}
}

func TestListJSON(t *testing.T) {
	code, out, _ := run(listFake(t), "list", "--json")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	golden(t, "list_json", out)
	var v struct {
		Groups []freezer.GroupView `json:"groups"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Groups) != 5 || v.Groups[0].Status != "busy" {
		t.Errorf("groups = %+v", v.Groups)
	}
}

func TestListQuery(t *testing.T) {
	// "acme" matches a cwd, "BACK" a name; queries are OR'd.
	code, out, errs := run(listFake(t), "list", "acme", "BACK")
	if code != 0 || errs != "" {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	golden(t, "list_query", out)

	code, out, _ = run(listFake(t), "list", "codex", "--json")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	golden(t, "list_query_json", out)

	// --all-procs still applies on top of a query.
	code, out, _ = run(listFake(t), "list", "--all-procs", "~/projects")
	if code != 0 || strings.Count(out, "  pids: ") != 1 || !strings.Contains(out, "  pids: 300 301 302 303\n") {
		t.Fatalf("code=%d out:\n%s", code, out)
	}
}

func TestListQueryNoMatch(t *testing.T) {
	code, out, errs := run(listFake(t), "list", "zzz")
	if code != 0 || errs != "" || strings.Count(out, "\n") != 1 || !strings.HasPrefix(out, "ID ") {
		t.Fatalf("code=%d stderr=%q out=%q", code, errs, out)
	}
	code, out, _ = run(listFake(t), "list", "zzz", "--json")
	if code != 0 || out != `{"groups":[]}`+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestListStatusColumn(t *testing.T) {
	_, out, _ := run(newFake(t), "list")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	hdr := strings.Fields(lines[0])
	want := []string{"ID", "NAME", "STATE", "STATUS", "RAM", "SWAP", "PROCS", "CWD"}
	if !slices.Equal(hdr, want) {
		t.Fatalf("header = %v", hdr)
	}
	col := strings.Index(lines[0], "STATUS")
	wantStatus := []string{"busy", "idle", "-", "shell"}
	for i, ws := range wantStatus {
		f := strings.Fields(lines[i+1][col:])
		if f[0] != ws {
			t.Errorf("row %d status = %q, want %q", i, f[0], ws)
		}
	}
}

func TestListEmptyJSON(t *testing.T) {
	code, out, _ := run(&fakeAPI{}, "list", "--json")
	if code != 0 || out != `{"groups":[]}`+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestFreezeAllSkipsSelfFrozenDaemon(t *testing.T) {
	f := newFake(t)
	code, _, _ := run(f, "freeze", "--all")
	if code != 0 || !slices.Equal(f.froze, []string{"102"}) {
		t.Fatalf("code=%d froze=%v", code, f.froze)
	}
	f = newFake(t)
	code, _, _ = run(f, "freeze", "--all", "--include-daemons")
	if code != 0 || !slices.Equal(f.froze, []string{"102", "200"}) {
		t.Fatalf("code=%d froze=%v", code, f.froze)
	}
}

func TestFreezeAllNothing(t *testing.T) {
	f := newFake(t)
	f.groups = f.groups[1:2] // only the frozen group
	code, out, _ := run(f, "freeze", "--all")
	if code != 0 || out != "nothing to freeze\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	code, out, _ = run(f, "freeze", "--all", "--json")
	if code != 0 || out != `{"results":[],"freed_bytes":0}`+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	f.groups[0].State = "running"
	code, out, _ = run(f, "thaw", "--all")
	if code != 0 || out != "nothing to thaw\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestThawAll(t *testing.T) {
	f := newFake(t)
	f.groups[2].State = "partial"
	code, out, _ := run(f, "thaw", "--all")
	if code != 0 || !slices.Equal(f.thawed, []string{"201", "200"}) {
		t.Fatalf("code=%d thawed=%v out=%q", code, f.thawed, out)
	}
	if !strings.Contains(out, "thawed claude back (201)\n") {
		t.Errorf("out = %q", out)
	}
}

func TestFreezeBySelector(t *testing.T) {
	f := newFake(t)
	code, out, _ := run(f, "freeze", "acme")
	if code != 0 || !slices.Equal(f.froze, []string{"102"}) {
		t.Fatalf("acme: code=%d froze=%v", code, f.froze)
	}
	want := "froze Acme web frontend builder (102): freed 300M — was busy\ntotal freed: 300M\n"
	if out != want {
		t.Errorf("out = %q, want %q", out, want)
	}

	f = newFake(t)
	if code, _, _ := run(f, "freeze", "102"); code != 0 || !slices.Equal(f.froze, []string{"102"}) {
		t.Fatalf("102: code=%d froze=%v", code, f.froze)
	}

	f = newFake(t)
	code, _, errs := run(f, "freeze", "nope")
	if code != 2 || len(f.freezeCalls) != 0 || !strings.Contains(errs, `afz: no agent matches "nope"`) {
		t.Fatalf("nope: code=%d stderr=%q", code, errs)
	}

	f = newFake(t)
	code, _, errs = run(f, "freeze", "claude")
	if code != 2 || len(f.freezeCalls) != 0 {
		t.Fatalf("claude: code=%d calls=%v", code, f.freezeCalls)
	}
	if !strings.Contains(errs, `"claude" matches several agents: 201 claude back; 200 claude daemon`) {
		t.Errorf("stderr = %q", errs)
	}
}

func TestFreezeDedup(t *testing.T) {
	f := newFake(t)
	code, _, _ := run(f, "freeze", "--include-daemons", "daemon", "acme", "102", "200")
	if code != 0 || !slices.Equal(f.froze, []string{"102", "200"}) {
		t.Fatalf("code=%d froze=%v", code, f.froze)
	}
}

func TestFreezeSelfByNameRequiresForce(t *testing.T) {
	f := newFake(t)
	code, out, _ := run(f, "freeze", "300")
	if code != 1 || len(f.froze) != 0 {
		t.Fatalf("code=%d froze=%v", code, f.froze)
	}
	if !slices.Equal(f.freezeCalls, []freezeCall{{"300", false}}) {
		t.Errorf("calls = %v", f.freezeCalls)
	}
	if !strings.HasPrefix(out, "failed me (300): refusing") {
		t.Errorf("out = %q", out)
	}

	f = newFake(t)
	code, _, _ = run(f, "freeze", "--force-self", "300")
	if code != 0 || !slices.Equal(f.freezeCalls, []freezeCall{{"300", true}}) {
		t.Fatalf("code=%d calls=%v", code, f.freezeCalls)
	}
}

func TestSwapGuard(t *testing.T) {
	f := newFake(t)
	f.status.SwapFree = 100 * mib
	code, _, errs := run(f, "freeze", "--all")
	if code != 1 || len(f.freezeCalls) != 0 {
		t.Fatalf("code=%d calls=%v", code, f.freezeCalls)
	}
	want := "afz: not enough free swap: need 400M, have 100M; rerun with --yes to freeze anyway (RAM will not be freed)\n"
	if errs != want {
		t.Errorf("stderr = %q, want %q", errs, want)
	}
	code, out, _ := run(f, "freeze", "--all", "--json")
	if code != 1 || len(f.freezeCalls) != 0 || !strings.Contains(out, `"error":"not enough free swap`) {
		t.Fatalf("json: code=%d out=%q", code, out)
	}

	code, _, _ = run(f, "freeze", "--all", "--yes")
	if code != 0 || !slices.Equal(f.froze, []string{"102"}) {
		t.Fatalf("--yes: code=%d froze=%v", code, f.froze)
	}
}

func TestBusyWarning(t *testing.T) {
	f := newFake(t)
	f.freezeResult = func(g freezer.GroupView, _ bool) freezer.Result {
		return freezer.Result{ID: g.ID, Name: g.Name, OK: true, Warning: "reclaim failed", RSSBefore: g.RSS, RSSAfter: g.RSS}
	}
	code, out, _ := run(f, "freeze", "102")
	if code != 0 || out != "froze Acme web frontend builder (102): freed 0B — reclaim failed; was busy\ntotal freed: 0B\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	_, out, _ = run(f, "freeze", "102", "--json")
	var v struct {
		Results []freezer.Result `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v.Results[0].Warning != "reclaim failed; was busy" {
		t.Errorf("warning = %q", v.Results[0].Warning)
	}

	// Failed freeze of a busy group still gets the warning; idle groups don't.
	f = newFake(t)
	f.freezeResult = func(g freezer.GroupView, _ bool) freezer.Result {
		return freezer.Result{ID: g.ID, Name: g.Name, Error: "boom"}
	}
	_, out, _ = run(f, "freeze", "102")
	if out != "failed Acme web frontend builder (102): boom — was busy\ntotal freed: 0B\n" {
		t.Errorf("out = %q", out)
	}
	f = newFake(t)
	_, out, _ = run(f, "freeze", "--include-daemons", "200")
	if strings.Contains(out, "busy") {
		t.Errorf("non-busy got warning: %q", out)
	}
	// Thaw never gets it.
	f = newFake(t)
	f.groups[0].State = "frozen"
	_, out, _ = run(f, "thaw", "102")
	if strings.Contains(out, "busy") {
		t.Errorf("thaw got warning: %q", out)
	}
}

func TestUsageErrors(t *testing.T) {
	cases := [][]string{
		{},
		{"freeze"},
		{"thaw"},
		{"freeze", "--all", "102"},
		{"list", "--bogus"},
		{"frob"},
		{"thaw", "--force-self", "102"},
		{"thaw", "--yes", "--all"},
		{"thaw", "--include-daemons", "--all"},
		{"list", "--yes"},
		{"list", "--all"},
		{"status", "--all-procs"},
		{"status", "x"},
		{"version", "--all"},
		{"freeze", "--all-procs", "--all"},
		{"help", "--yes"},
		{"freeze", "-y", "102"},
	}
	for _, args := range cases {
		f := newFake(t)
		code, out, errs := run(f, args...)
		if code != 2 {
			t.Errorf("%v: code=%d", args, code)
		}
		if out != "" || !strings.HasPrefix(errs, "afz: ") || !strings.Contains(errs, "usage:") {
			t.Errorf("%v: stdout=%q stderr=%q", args, out, errs)
		}
		if len(f.freezeCalls)+len(f.thawed) != 0 {
			t.Errorf("%v: acted", args)
		}
		if slices.Contains(args, "--json") {
			continue
		}
		jargs := append(slices.Clone(args), "--json")
		code, out, errs = run(f, jargs...)
		if code != 2 || errs != "" {
			t.Errorf("%v: code=%d stderr=%q", jargs, code, errs)
		}
		var v map[string]string
		if err := json.Unmarshal([]byte(out), &v); err != nil || v["error"] == "" || len(v) != 1 {
			t.Errorf("%v: stdout=%q err=%v", jargs, out, err)
		}
	}
}

func TestUnknownCommandJSON(t *testing.T) {
	code, out, errs := run(newFake(t), "--json", "frob")
	// "--json" as first arg is not a command.
	if code != 2 || errs != "" {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	var v struct{ Error string }
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Error == "" {
		t.Fatalf("out=%q", out)
	}
	code, out, _ = run(newFake(t), "frob", "--json")
	if code != 2 || out != `{"error":"unknown command \"frob\""}`+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestAPIErrors(t *testing.T) {
	for _, args := range [][]string{{"list"}, {"freeze", "--all"}, {"thaw", "102"}} {
		f := newFake(t)
		f.listErr = errors.New("proc gone")
		code, out, errs := run(f, args...)
		if code != 1 || out != "" || errs != "afz: proc gone\n" {
			t.Errorf("%v: code=%d out=%q errs=%q", args, code, out, errs)
		}
		code, out, _ = run(f, append(args, "--json")...)
		if code != 1 || out != `{"error":"proc gone"}`+"\n" {
			t.Errorf("%v json: code=%d out=%q", args, code, out)
		}
	}
	for _, args := range [][]string{{"status"}, {"freeze", "--all"}} {
		f := newFake(t)
		f.statusErr = errors.New("no meminfo")
		code, _, errs := run(f, args...)
		if code != 1 || errs != "afz: no meminfo\n" || len(f.freezeCalls) != 0 {
			t.Errorf("%v: code=%d errs=%q", args, code, errs)
		}
	}
}

func TestJSONShapes(t *testing.T) {
	f := newFake(t)
	code, out, _ := run(f, "freeze", "--all", "--include-daemons", "--json")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var fr struct {
		Results []freezer.Result `json:"results"`
		Freed   *uint64          `json:"freed_bytes"`
	}
	if err := json.Unmarshal([]byte(out), &fr); err != nil {
		t.Fatal(err)
	}
	if len(fr.Results) != 2 || fr.Freed == nil || *fr.Freed != 300*mib+(50*mib-50*mib/4) {
		t.Errorf("freeze json = %s", out)
	}

	f = newFake(t)
	code, out, _ = run(f, "thaw", "--all", "--json")
	var th struct {
		Results []freezer.Result `json:"results"`
		Freed   *uint64          `json:"freed_bytes"`
	}
	if err := json.Unmarshal([]byte(out), &th); err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if len(th.Results) != 1 || th.Results[0].ID != "201" || th.Freed == nil || *th.Freed != 0 {
		t.Errorf("thaw json = %s", out)
	}

	code, out, _ = run(f, "status", "--json")
	var st freezer.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil || code != 0 || st != f.status {
		t.Errorf("status json = %s err=%v", out, err)
	}

	code, out, _ = run(f, "version", "--json")
	if code != 0 || out != `{"version":"dev"}`+"\n" {
		t.Errorf("version json = %q", out)
	}
}

func TestStatusText(t *testing.T) {
	code, out, _ := run(newFake(t), "status")
	want := "frozen: 1 groups, freed 250M, in swap 250M\nRAM available 8.0G, swap free 16.0G\n"
	if code != 0 || out != want {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestVersionAndHelp(t *testing.T) {
	code, out, _ := run(newFake(t), "version")
	if code != 0 || out != "afz dev\n" {
		t.Fatalf("version: code=%d out=%q", code, out)
	}
	for _, h := range []string{"help", "-h", "--help"} {
		code, out, _ := run(newFake(t), h)
		if code != 0 {
			t.Errorf("%s: code=%d", h, code)
		}
		if n := strings.Count(out, "\n"); n > 25 {
			t.Errorf("%s: %d lines", h, n)
		}
		for _, w := range []string{"list", "freeze", "thaw", "status", "version", "help",
			"--json", "--all", "--include-daemons", "--yes", "--force-self", "--all-procs"} {
			if !strings.Contains(out, w) {
				t.Errorf("%s: help lacks %q", h, w)
			}
		}
	}
}

func TestExitCodePartialFailure(t *testing.T) {
	f := newFake(t)
	f.freezeResult = func(g freezer.GroupView, _ bool) freezer.Result {
		if g.ID == "200" {
			return freezer.Result{ID: g.ID, Name: g.Name, Error: "cgroup move failed"}
		}
		return freezer.Result{ID: g.ID, Name: g.Name, OK: true, RSSBefore: g.RSS, RSSAfter: 0}
	}
	code, out, _ := run(f, "freeze", "--all", "--include-daemons")
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	want := "froze Acme web frontend builder (102): freed 400M — was busy\n" +
		"failed claude daemon (200): cgroup move failed\n" +
		"total freed: 400M\n"
	if out != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}

func TestFreedClampedAtZero(t *testing.T) {
	f := newFake(t)
	f.freezeResult = func(g freezer.GroupView, _ bool) freezer.Result {
		return freezer.Result{ID: g.ID, Name: g.Name, OK: true, RSSBefore: 10, RSSAfter: 20}
	}
	_, out, _ := run(f, "freeze", "102", "--json")
	if !strings.HasSuffix(out, `"freed_bytes":0}`+"\n") {
		t.Errorf("out = %q", out)
	}
}

func TestTruncName(t *testing.T) {
	cases := map[string]string{
		"short":                  "short",
		strings.Repeat("é", 40):  strings.Repeat("é", 40),
		strings.Repeat("é", 41):  strings.Repeat("é", 39) + "…",
		strings.Repeat("x", 100): strings.Repeat("x", 39) + "…",
	}
	for in, want := range cases {
		if got := truncName(in); got != want {
			t.Errorf("truncName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListControlCharsInName(t *testing.T) {
	f := newFake(t)
	f.groups = f.groups[:1]
	f.groups[0].Name = "evil\tname\nhere"
	_, out, _ := run(f, "list")
	if n := strings.Count(out, "\n"); n != 2 || !strings.Contains(out, "evil name here") {
		t.Errorf("out = %q", out)
	}
}
