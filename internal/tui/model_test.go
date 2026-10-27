package tui

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/DataMata2/agent-freeze/internal/discover"
	"github.com/DataMata2/agent-freeze/internal/freezer"
)

const gib = 1 << 30

// fakeAPI is an in-memory freezer.API. It never touches processes.
type fakeAPI struct {
	groups  []freezer.GroupView
	status  freezer.Status
	listErr error
	fail    map[string]string // key -> error message for Freeze/Thaw
	froze   []string
	thawed  []string
}

func (f *fakeAPI) List() ([]freezer.GroupView, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]freezer.GroupView(nil), f.groups...), nil
}

func (f *fakeAPI) Status() (freezer.Status, error) { return f.status, nil }

func (f *fakeAPI) Freeze(g freezer.GroupView, forceSelf bool) freezer.Result {
	if forceSelf {
		panic("tui must never pass forceSelf")
	}
	f.froze = append(f.froze, g.Key)
	r := freezer.Result{ID: g.ID, Key: g.Key, Name: g.Name, RSSBefore: g.RSS, RSSAfter: g.RSS / 4}
	if e, bad := f.fail[g.Key]; bad {
		r.Error = e
		return r
	}
	r.OK = true
	return r
}

func (f *fakeAPI) Thaw(g freezer.GroupView) freezer.Result {
	f.thawed = append(f.thawed, g.Key)
	r := freezer.Result{ID: g.ID, Key: g.Key, Name: g.Name}
	if e, bad := f.fail[g.Key]; bad {
		r.Error = e
		return r
	}
	r.OK = true
	return r
}

func gv(name string, rss uint64, state string) freezer.GroupView {
	return freezer.GroupView{
		Group: discover.Group{
			ID: name + "-id", Key: name + "-key", Agent: "claude", Name: name,
			Cwd: "/home/u/src/" + name, RootPID: 100, PIDs: []int{100, 101}, RSS: rss,
			Status: "idle",
		},
		State: state,
	}
}

// fixture: alpha(running), beta(self), gamma(daemon), delta(frozen), eps(partial).
func newFake() *fakeAPI {
	alpha := gv("alpha", 1*gib, "running")
	beta := gv("beta", 1*gib, "running")
	beta.Self = true
	gamma := gv("gamma", 1*gib, "running")
	gamma.Daemon = true
	delta := gv("delta", 1*gib, "frozen")
	eps := gv("eps", 1*gib, "partial")
	return &fakeAPI{
		groups: []freezer.GroupView{alpha, beta, gamma, delta, eps},
		status: freezer.Status{Frozen: 1, Freed: gib, MemAvailable: 8 * gib, SwapFree: 16 * gib},
		fail:   map[string]string{},
	}
}

func TestMain(m *testing.M) {
	// Never sleep on a real timer: a tick command yields its message at
	// once, and drain drops tickMsg/spinMsg instead of re-feeding them.
	tick = func(_ time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		return func() tea.Msg { return fn(time.Time{}) }
	}
	dir, err := os.MkdirTemp("", "afz-tui-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_STATE_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// feed delivers msg to m and drains every resulting command synchronously.
// Tick and spinner messages are dropped so the drain terminates.
func feed(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	nm, cmd := m.Update(msg)
	m = nm.(model)
	return drain(t, m, cmd)
}

func drain(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case nil, tickMsg, spinMsg:
		return m
	case tea.BatchMsg:
		for _, c := range msg {
			m = drain(t, m, c)
		}
		return m
	case tea.QuitMsg:
		return m
	default:
		return feed(t, m, msg)
	}
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// plainView renders m with SGR escape codes removed, so width and content
// checks do not depend on whether lipgloss detects a colour terminal.
func plainView(m model) string { return ansiRE.ReplaceAllString(m.View(), "") }

func key(s string) tea.KeyMsg {
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(t *testing.T, m model, keys ...string) model {
	t.Helper()
	for _, k := range keys {
		m = feed(t, m, key(k))
	}
	return m
}

func loaded(t *testing.T, api freezer.API) model {
	t.Helper()
	m := newModel(api)
	m = feed(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drain(t, m, m.refreshCmd())
	if len(m.groups) == 0 {
		t.Fatal("no groups after refresh")
	}
	return m
}

func TestCursorClamps(t *testing.T) {
	m := loaded(t, newFake())
	m = press(t, m, "up", "k")
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}
	m = press(t, m, "down", "j", "j", "j", "j", "j", "down", "j")
	if m.cursor != 4 {
		t.Fatalf("cursor = %d, want 4", m.cursor)
	}
	m = press(t, m, "k")
	if m.cursor != 3 {
		t.Fatalf("cursor = %d, want 3", m.cursor)
	}
}

func TestSpaceAndSelectAll(t *testing.T) {
	m := loaded(t, newFake())
	m = press(t, m, "space")
	if !m.selected["alpha-key"] {
		t.Fatal("space did not select alpha")
	}
	m = press(t, m, "space")
	if m.selected["alpha-key"] {
		t.Fatal("second space did not deselect alpha")
	}
	m = press(t, m, "a")
	for _, k := range []string{"alpha-key", "delta-key", "eps-key"} {
		if !m.selected[k] {
			t.Errorf("a did not select %s", k)
		}
	}
	for _, k := range []string{"beta-key", "gamma-key"} {
		if m.selected[k] {
			t.Errorf("a selected self/daemon %s", k)
		}
	}
	m = press(t, m, "a")
	if n := countSelected(m); n != 0 {
		t.Fatalf("second a left %d selected", n)
	}
}

func countSelected(m model) int {
	n := 0
	for _, v := range m.selected {
		if v {
			n++
		}
	}
	return n
}

func TestFreezeCurrentRow(t *testing.T) {
	api := newFake()
	m := loaded(t, api)
	m = press(t, m, "f")
	if len(api.froze) != 1 || api.froze[0] != "alpha-key" {
		t.Fatalf("froze = %v, want [alpha-key]", api.froze)
	}
	if !strings.Contains(m.msg, "froze 1") {
		t.Fatalf("msg = %q, want froze 1", m.msg)
	}
	if !strings.Contains(m.msg, "freed 768M") {
		t.Fatalf("msg = %q, want freed 768M", m.msg)
	}
	if m.busy {
		t.Fatal("busy still set after opDone")
	}
}

func TestFreezeStartsBusy(t *testing.T) {
	m := loaded(t, newFake())
	nm, cmd := m.Update(key("f"))
	m = nm.(model)
	if !m.busy || cmd == nil {
		t.Fatalf("f: busy=%v cmd=%v, want busy with a command", m.busy, cmd != nil)
	}
}

func TestSelfRowRefused(t *testing.T) {
	api := newFake()
	m := loaded(t, api)
	m = press(t, m, "j", "f") // cursor on beta (self)
	if len(api.froze) != 0 {
		t.Fatalf("froze = %v, want nothing", api.froze)
	}
	want := "that's this agent's own session — use the CLI with --force-self"
	if m.msg != want {
		t.Fatalf("msg = %q, want %q", m.msg, want)
	}
	if m.busy || m.confirm != nil {
		t.Fatal("self refusal must not start an operation or prompt")
	}

	// Self among selected rows also refuses the whole freeze.
	m = press(t, m, "space", "k", "space", "f")
	if len(api.froze) != 0 {
		t.Fatalf("froze = %v with self selected, want nothing", api.froze)
	}
	if m.msg != want {
		t.Fatalf("msg = %q, want %q", m.msg, want)
	}

	// F skips self (and daemon) and freezes only eligible non-frozen rows.
	m = press(t, m, "F")
	if strings.Join(api.froze, ",") != "alpha-key,eps-key" {
		t.Fatalf("F froze %v, want [alpha-key eps-key]", api.froze)
	}
}

func TestSwapConfirm(t *testing.T) {
	api := newFake()
	api.status.SwapFree = 512 << 20
	m := loaded(t, api)
	m = press(t, m, "f")
	if m.confirm == nil {
		t.Fatal("no confirm with low swap")
	}
	if !strings.Contains(plainView(m), "Not enough free swap (need 1.0G, have 512M). Freeze anyway? y/n") {
		t.Fatalf("view lacks swap prompt:\n%s", plainView(m))
	}
	m = press(t, m, "n")
	if len(api.froze) != 0 || m.confirm != nil {
		t.Fatalf("n: froze=%v confirm=%v", api.froze, m.confirm)
	}
	if m.msg != "cancelled" {
		t.Fatalf("msg = %q, want cancelled", m.msg)
	}
	m = press(t, m, "f", "y")
	if len(api.froze) != 1 || api.froze[0] != "alpha-key" {
		t.Fatalf("y: froze = %v", api.froze)
	}
	if m.confirm != nil {
		t.Fatal("confirm not cleared after y")
	}
}

func TestBusyConfirm(t *testing.T) {
	api := newFake()
	api.groups[0].Status = "busy"
	api.groups[4].Status = "busy"
	m := loaded(t, api)
	m = press(t, m, "F")
	if m.confirm == nil {
		t.Fatal("no busy confirm")
	}
	if !strings.Contains(plainView(m), "2 of the selected agents are busy (mid-task). Freeze anyway? y/n") {
		t.Fatalf("view lacks busy prompt:\n%s", plainView(m))
	}
	m = press(t, m, "n")
	if len(api.froze) != 0 || m.confirm != nil || m.msg != "cancelled" {
		t.Fatalf("n: froze=%v confirm=%v msg=%q", api.froze, m.confirm, m.msg)
	}

	m = press(t, m, "F", "y")
	if strings.Join(api.froze, ",") != "alpha-key,eps-key" {
		t.Fatalf("y: froze = %v", api.froze)
	}

	// Busy guard passes, then the swap guard still applies.
	api2 := newFake()
	api2.groups[0].Status = "busy"
	api2.status.SwapFree = 0
	m = loaded(t, api2)
	m = press(t, m, "f", "y")
	if m.confirm == nil || !strings.Contains(plainView(m), "Not enough free swap") {
		t.Fatalf("swap guard not shown after busy y:\n%s", plainView(m))
	}
	if len(api2.froze) != 0 {
		t.Fatalf("froze before swap confirm: %v", api2.froze)
	}
	m = press(t, m, "y")
	if len(api2.froze) != 1 {
		t.Fatalf("froze = %v after both confirms", api2.froze)
	}
}

func TestThawTargets(t *testing.T) {
	api := newFake()
	m := loaded(t, api)
	m = press(t, m, "U")
	if strings.Join(api.thawed, ",") != "delta-key,eps-key" {
		t.Fatalf("U thawed %v", api.thawed)
	}
	if m.msg != "thawed 2" {
		t.Fatalf("msg = %q, want thawed 2", m.msg)
	}
	m = press(t, m, "j", "j", "j", "u")
	if api.thawed[len(api.thawed)-1] != "delta-key" {
		t.Fatalf("u thawed %v", api.thawed)
	}
}

func TestOpDoneSummaryWithFailure(t *testing.T) {
	api := newFake()
	api.fail["eps-key"] = "permission denied"
	m := loaded(t, api)
	m = press(t, m, "a", "f")
	// alpha ok, delta (already frozen, ok), eps failed.
	want := "froze 2, freed 1.5G; failed: eps: permission denied"
	if m.msg != want {
		t.Fatalf("msg = %q, want %q", m.msg, want)
	}
	if countSelected(m) != 0 {
		t.Fatal("selection not cleared after op")
	}

	api.fail["alpha-key"] = "boom"
	api.fail["delta-key"] = "gone"
	m = press(t, m, "a", "u")
	want = "failed: alpha: boom (+2 more)"
	if m.msg != want {
		t.Fatalf("msg = %q, want %q", m.msg, want)
	}
}

func TestBusyIgnoresKeys(t *testing.T) {
	api := newFake()
	m := loaded(t, api)
	m.busy = true
	for _, k := range []string{"j", "space", "a", "f", "u", "F", "U", "r", "/"} {
		nm, cmd := m.Update(key(k))
		m = nm.(model)
		if cmd != nil {
			t.Errorf("key %q returned a command while busy", k)
		}
	}
	if m.cursor != 0 || countSelected(m) != 0 || len(api.froze) != 0 || len(api.thawed) != 0 || m.filtering {
		t.Fatalf("state changed while busy: cursor=%d sel=%d", m.cursor, countSelected(m))
	}
	_, cmd := m.Update(key("q"))
	if cmd == nil {
		t.Fatal("q while busy returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q while busy did not quit")
	}
	_, cmd = m.Update(key("ctrl+c"))
	if cmd == nil {
		t.Fatal("ctrl+c returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c did not quit")
	}
}

func TestTickSkipsRefreshWhileBusy(t *testing.T) {
	api := newFake()
	m := loaded(t, api)
	api.groups = api.groups[:1]
	m.busy = true
	m = feed(t, m, tickMsg{})
	if len(m.groups) != 5 {
		t.Fatalf("refreshed while busy: %d groups", len(m.groups))
	}
	m.busy = false
	m = feed(t, m, tickMsg{})
	if len(m.groups) != 1 {
		t.Fatalf("tick did not refresh: %d groups", len(m.groups))
	}
}

func TestRefreshErrorKeepsList(t *testing.T) {
	api := newFake()
	m := loaded(t, api)
	api.listErr = errors.New("proc unreadable")
	m = feed(t, m, tickMsg{})
	if len(m.groups) != 5 {
		t.Fatalf("lost list on error: %d groups", len(m.groups))
	}
	v := plainView(m)
	if !strings.Contains(v, "proc unreadable") || !strings.Contains(v, "alpha") {
		t.Fatalf("view should show error and last list:\n%s", v)
	}
	api.listErr = nil
	m = feed(t, m, tickMsg{})
	if m.err != nil || strings.Contains(plainView(m), "proc unreadable") {
		t.Fatal("error not cleared after successful retry")
	}
}

func TestViewContainsRows(t *testing.T) {
	m := loaded(t, newFake())
	v := plainView(m)
	t.Logf("View() at width 120:\n%s", v)
	for _, s := range []string{"alpha", "beta", "gamma", "delta", "eps", "(you)", "[daemon]",
		"agent-freeze", "RAM free 8.0G", "swap free 16.0G", "frozen 1 (freed 1.0G)"} {
		if !strings.Contains(v, s) {
			t.Errorf("view missing %q", s)
		}
	}
	for i, line := range strings.Split(v, "\n") {
		if w := len([]rune(line)); w > 120 {
			t.Errorf("line %d is %d wide, > 120: %q", i, w, line)
		}
	}
}

func TestViewShowsStatus(t *testing.T) {
	api := newFake()
	api.groups[0].Status = "busy"
	api.groups[3].Status = ""
	m := loaded(t, api)
	lines := strings.Split(plainView(m), "\n")
	var alpha, delta string
	for _, l := range lines {
		if strings.Contains(l, "alpha") {
			alpha = l
		}
		if strings.Contains(l, "delta") {
			delta = l
		}
	}
	if !strings.Contains(alpha, "busy") {
		t.Errorf("alpha row lacks busy status: %q", alpha)
	}
	if !strings.Contains(delta, " - ") {
		t.Errorf("delta row lacks '-' status: %q", delta)
	}
	if !strings.Contains(plainView(m), "STATUS") {
		t.Error("view lacks STATUS column header")
	}
}

func TestViewTruncatesNarrow(t *testing.T) {
	api := newFake()
	api.groups[0].Name = strings.Repeat("n", 80)
	api.groups[0].Cwd = "/" + strings.Repeat("c", 200)
	m := loaded(t, api)
	m = feed(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	for i, line := range strings.Split(plainView(m), "\n") {
		if w := len([]rune(line)); w > 80 {
			t.Errorf("line %d is %d wide, > 80", i, w)
		}
	}
	m.width = 0
	for i, line := range strings.Split(plainView(m), "\n") {
		if w := len([]rune(line)); w > 100 {
			t.Errorf("width 0: line %d is %d wide, > 100", i, w)
		}
	}
}

func TestViewStyled(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(2) // termenv.ANSI
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	api := newFake()
	api.groups[0].Status = "busy"
	m := loaded(t, api)
	raw := m.View()
	if !strings.Contains(raw, "\x1b[") {
		t.Fatal("styled view has no escape codes")
	}
	v := plainView(m)
	for _, s := range []string{"alpha", "busy", "(you)", "[daemon]"} {
		if !strings.Contains(v, s) {
			t.Errorf("styled view missing %q", s)
		}
	}
	for i, line := range strings.Split(v, "\n") {
		if w := len([]rune(line)); w > 120 {
			t.Errorf("line %d is %d wide, > 120", i, w)
		}
	}
}

func TestSpinnerIgnoresStaleTick(t *testing.T) {
	m := loaded(t, newFake())
	m.busy, m.opGen = true, 2
	nm, cmd := m.Update(spinMsg{gen: 1})
	if m2 := nm.(model); m2.frame != 0 || cmd != nil {
		t.Fatalf("stale spin tick: frame=%d cmd=%v", m2.frame, cmd != nil)
	}
	nm, cmd = m.Update(spinMsg{gen: 2})
	if m2 := nm.(model); m2.frame != 1 || cmd == nil {
		t.Fatalf("current spin tick: frame=%d cmd=%v", m2.frame, cmd != nil)
	}
	if !strings.Contains(plainView(nm.(model)), "working…") {
		t.Error("busy view lacks spinner line")
	}
}

func TestHelpWrapsInsteadOfCuttingKeysOff(t *testing.T) {
	m := loaded(t, newFake())
	m = feed(t, m, tea.WindowSizeMsg{Width: 80, Height: 40})
	v := plainView(m)
	for _, want := range []string{"s settings", "q quit", "m auto/manual"} {
		if !strings.Contains(v, want) {
			t.Errorf("help at width 80 lacks %q:\n%s", want, v)
		}
	}
	for i, line := range strings.Split(v, "\n") {
		if n := len([]rune(line)); n > 80 {
			t.Errorf("line %d is %d wide, > 80: %q", i, n, line)
		}
	}
}
