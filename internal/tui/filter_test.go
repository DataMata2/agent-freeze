package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func keyName(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return key(s)
}

func pressKeys(t *testing.T, m model, keys ...string) model {
	t.Helper()
	for _, k := range keys {
		m = feed(t, m, keyName(k))
	}
	return m
}

func visibleKeys(m model) string {
	var ks []string
	for _, g := range m.visible() {
		ks = append(ks, g.Key)
	}
	return strings.Join(ks, ",")
}

func TestFilterPromptTakesEveryKey(t *testing.T) {
	api := newFake()
	m := loaded(t, api)
	m = press(t, m, "/")
	if !m.filtering {
		t.Fatal("/ did not open the prompt")
	}
	nm, cmd := m.Update(key("q"))
	m = nm.(model)
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("q quit while the prompt was open")
		}
	}
	m = press(t, m, "f", "j", "F", "a", "space", "u")
	if m.filter != "qfjFa u" {
		t.Fatalf("filter = %q, want %q", m.filter, "qfjFa u")
	}
	if len(api.froze)+len(api.thawed) != 0 || countSelected(m) != 0 || m.cursor != 0 {
		t.Fatalf("prompt keys acted: froze=%v thawed=%v sel=%d cursor=%d",
			api.froze, api.thawed, countSelected(m), m.cursor)
	}
	if !strings.Contains(plainView(m), "/qfjFa u") {
		t.Fatalf("status line lacks prompt:\n%s", plainView(m))
	}
	_, cmd = m.Update(key("ctrl+c"))
	if cmd == nil {
		t.Fatal("ctrl+c in prompt returned no command")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatal("ctrl+c in prompt did not quit")
	}
}

func TestFilterLiveEditAndEsc(t *testing.T) {
	m := loaded(t, newFake())
	m = press(t, m, "/", "a", "l", "x")
	if got := visibleKeys(m); got != "" {
		t.Fatalf("visible = %q, want none", got)
	}
	if !strings.Contains(plainView(m), "no match") {
		t.Fatalf("empty result lacks no match:\n%s", plainView(m))
	}
	m = feed(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\n\t"), Paste: true})
	m = pressKeys(t, m, "backspace")
	if m.filter != "al" || visibleKeys(m) != "alpha-key" {
		t.Fatalf("filter=%q visible=%q", m.filter, visibleKeys(m))
	}
	v := plainView(m)
	if !strings.Contains(v, "filter: al  (1/5)") {
		t.Fatalf("header lacks filter line:\n%s", v)
	}
	if strings.Contains(v, "beta") {
		t.Fatalf("hidden row rendered:\n%s", v)
	}

	// Esc in the prompt closes it and clears the filter.
	m = pressKeys(t, m, "esc")
	if m.filtering || m.filter != "" || len(m.visible()) != 5 {
		t.Fatalf("esc: filtering=%v filter=%q visible=%d", m.filtering, m.filter, len(m.visible()))
	}
	if strings.Contains(plainView(m), "filter:") {
		t.Fatal("header still shows a filter")
	}

	// Enter keeps the filter; then keys act again, and Esc clears it.
	m = press(t, m, "/", "l", "t")
	m = pressKeys(t, m, "enter")
	if m.filtering || m.filter != "lt" || visibleKeys(m) != "delta-key" {
		t.Fatalf("enter: filtering=%v filter=%q visible=%q", m.filtering, m.filter, visibleKeys(m))
	}
	m = press(t, m, "space")
	if !m.selected["delta-key"] {
		t.Fatal("space after enter did not select the visible row")
	}
	m = pressKeys(t, m, "esc")
	if m.filter != "" || len(m.visible()) != 5 {
		t.Fatalf("esc with closed prompt: filter=%q visible=%d", m.filter, len(m.visible()))
	}
}

func TestFilterFreezeVisibleOnly(t *testing.T) {
	api := newFake()
	m := loaded(t, api)
	// Select alpha, then filter it out: hidden selections are kept but
	// never acted on.
	m = press(t, m, "space", "/", "p", "s")
	m = pressKeys(t, m, "enter")
	if visibleKeys(m) != "eps-key" {
		t.Fatalf("visible = %q", visibleKeys(m))
	}
	m = press(t, m, "F")
	if strings.Join(api.froze, ",") != "eps-key" {
		t.Fatalf("F froze %v, want [eps-key]", api.froze)
	}

	// With only hidden rows selected, f and u act on nothing: the cursor
	// row was never picked.
	api.froze = nil
	m.selected["alpha-key"] = true
	m.selected["delta-key"] = true
	m = press(t, m, "f")
	if len(api.froze) != 0 || m.busy || m.confirm != nil {
		t.Fatalf("f with hidden selection froze %v", api.froze)
	}
	if m.msg != "selected rows are hidden by the filter" {
		t.Fatalf("f msg = %q", m.msg)
	}
	m.msg = ""
	m = press(t, m, "u")
	if len(api.thawed) != 0 || m.busy {
		t.Fatalf("u with hidden selection thawed %v", api.thawed)
	}
	if m.msg != "selected rows are hidden by the filter" {
		t.Fatalf("u msg = %q", m.msg)
	}
	m.selected = map[string]bool{}

	// "ta" shows beta (self) and delta (frozen): nothing for F to freeze.
	api.froze = nil
	m = pressKeys(t, m, "esc")
	m = press(t, m, "/", "t", "a")
	m = pressKeys(t, m, "enter")
	m = press(t, m, "F")
	if len(api.froze) != 0 || m.msg != "nothing to freeze" {
		t.Fatalf("F froze %v msg=%q, want nothing", api.froze, m.msg)
	}
}

func TestFilterSelectAllVisible(t *testing.T) {
	m := loaded(t, newFake())
	m = press(t, m, "space") // alpha, about to be hidden
	m = press(t, m, "/", "t", "a")
	m = pressKeys(t, m, "enter")
	m = press(t, m, "a")
	if !m.selected["delta-key"] || m.selected["beta-key"] {
		t.Fatalf("a on filter: selected=%v", m.selected)
	}
	if !m.selected["alpha-key"] {
		t.Fatal("a dropped a hidden selection")
	}
	m = press(t, m, "a")
	if m.selected["delta-key"] || !m.selected["alpha-key"] {
		t.Fatalf("second a: selected=%v", m.selected)
	}
	// U thaws visible frozen rows only: delta, not eps.
	api := m.api.(*fakeAPI)
	m = press(t, m, "U")
	if strings.Join(api.thawed, ",") != "delta-key" {
		t.Fatalf("U thawed %v, want [delta-key]", api.thawed)
	}
}

func TestFilterCursorStableAcrossRefresh(t *testing.T) {
	api := newFake()
	m := loaded(t, api)
	m = press(t, m, "/", "t", "a")
	m = pressKeys(t, m, "enter")
	m = press(t, m, "j", "j", "j") // clamps on delta, the 2nd visible row
	if g, _ := m.current(); g.Key != "delta-key" || m.cursor != 1 {
		t.Fatalf("cursor=%d on %q", m.cursor, g.Key)
	}

	// beta disappears: the cursor follows delta to row 0.
	api.groups = append(api.groups[:1:1], api.groups[2:]...)
	m = feed(t, m, tickMsg{})
	if m.filter != "ta" {
		t.Fatalf("refresh lost the filter: %q", m.filter)
	}
	if g, _ := m.current(); g.Key != "delta-key" || m.cursor != 0 {
		t.Fatalf("after refresh cursor=%d on %q", m.cursor, g.Key)
	}

	// Without a filter the cursor also keeps its group.
	m = pressKeys(t, m, "esc")
	if g, _ := m.current(); g.Key != "delta-key" {
		t.Fatalf("clearing the filter moved the cursor to %q", g.Key)
	}
	api.groups = api.groups[1:] // drop alpha
	m = feed(t, m, tickMsg{})
	if g, _ := m.current(); g.Key != "delta-key" || m.cursor != 1 {
		t.Fatalf("unfiltered refresh: cursor=%d on %q", m.cursor, g.Key)
	}

	// The group vanishes: the cursor clamps.
	m = press(t, m, "j", "j", "j")
	api.groups = api.groups[:2]
	m = feed(t, m, tickMsg{})
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want clamped to 1", m.cursor)
	}
}

func TestHelpListsSearch(t *testing.T) {
	if !strings.Contains(plainView(loaded(t, newFake())), "/ search") {
		t.Fatal("help line lacks / search")
	}
}

func TestFilterConfirmKeepsCapturedTargets(t *testing.T) {
	api := newFake()
	api.groups[0].Status = "busy" // alpha
	m := loaded(t, api)
	m = press(t, m, "/", "p", "h")
	m = pressKeys(t, m, "enter")
	m = press(t, m, "F")
	if m.confirm == nil {
		t.Fatal("no busy confirm")
	}
	// A new group matching the filter shows up while the prompt waits.
	extra := gv("alphabet", gib, "running")
	api.groups = append(api.groups, extra)
	m = feed(t, m, tickMsg{})
	if visibleKeys(m) != "alpha-key,alphabet-key" || m.confirm == nil {
		t.Fatalf("refresh: visible=%q confirm=%v", visibleKeys(m), m.confirm != nil)
	}
	m = press(t, m, "y")
	if strings.Join(api.froze, ",") != "alpha-key" {
		t.Fatalf("froze %v, want only the captured [alpha-key]", api.froze)
	}
}
