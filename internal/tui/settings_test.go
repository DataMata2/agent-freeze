package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DataMata2/agent-freeze/internal/auto"
)

func savedSettings(t *testing.T) auto.Config {
	t.Helper()
	c, err := auto.LoadConfig(auto.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func settingsLine(t *testing.T, m model, label string) string {
	t.Helper()
	for _, l := range strings.Split(plainView(m), "\n") {
		if strings.Contains(l, label) {
			return l
		}
	}
	t.Fatalf("no %q line in:\n%s", label, plainView(m))
	return ""
}

func TestSKeyOpensAndClosesTheSettingsScreen(t *testing.T) {
	freshState(t)
	m := press(t, loaded(t, newFake()), "s", "j")
	v := plainView(m)
	t.Logf("settings screen at width 120:\n%s", v)
	for _, want := range []string{"Auto mode settings", "mode", "idle time", "15 min", "memory trigger", "warn", "thaw on tab"} {
		if !strings.Contains(v, want) {
			t.Errorf("settings screen lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "NAME") {
		t.Error("settings screen still shows the session table")
	}
	for _, back := range []string{"s", "esc"} {
		m = press(t, loaded(t, newFake()), "s", back)
		if !strings.Contains(plainView(m), "NAME") {
			t.Errorf("%s did not go back to the sessions", back)
		}
	}
}

func TestArrowsChangeTheSelectedSetting(t *testing.T) {
	freshState(t)
	m := press(t, loaded(t, newFake()), "s", "j", "right")
	if got := savedSettings(t).IdleMinutes; got != 20 {
		t.Fatalf("idle = %v after right, want 20", got)
	}
	if line := settingsLine(t, m, "idle time"); !strings.Contains(line, "20 min") || !strings.HasPrefix(line, ">") {
		t.Errorf("idle line = %q", line)
	}
	press(t, m, "j", "j", "left")
	if got := savedSettings(t).MemoryLevel; got != "off" {
		t.Fatalf("memory = %q after left from warn, want off", got)
	}
}

func TestEnterTypesAValue(t *testing.T) {
	freshState(t)
	m := press(t, loaded(t, newFake()), "s", "j", "enter", "4", "5", "6", "backspace", "enter")
	if got := savedSettings(t).IdleMinutes; got != 45 {
		t.Fatalf("idle = %v, want 45", got)
	}
	m = press(t, m, "enter", "a", "b", "c", "enter")
	if got := savedSettings(t).IdleMinutes; got != 45 || !strings.Contains(m.msg, "not a number") {
		t.Fatalf("idle = %v, msg %q: a bad value must be refused", got, m.msg)
	}
	m = press(t, m, "enter", "9", "esc")
	if got := savedSettings(t).IdleMinutes; got != 45 || m.editing {
		t.Fatalf("idle = %v editing=%v: esc must cancel", got, m.editing)
	}
}

func TestTypingQDoesNotQuit(t *testing.T) {
	freshState(t)
	m := press(t, loaded(t, newFake()), "s", "j", "j", "j", "enter")
	nm, cmd := m.Update(key("q"))
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("q quit while typing")
		}
	}
	if got := nm.(model).input; got != "q" {
		t.Errorf("input = %q, want q", got)
	}
}

func TestModeRowSwitchesAutoMode(t *testing.T) {
	freshState(t)
	m := loaded(t, newFake())
	var calls []bool
	m.setAuto = func(on bool) error { calls = append(calls, on); return nil }
	press(t, m, "s", "enter")
	if len(calls) != 1 || !calls[0] {
		t.Fatalf("setAuto calls = %v, want [true]", calls)
	}
}

func TestSessionKeysDoNothingOnTheSettingsScreen(t *testing.T) {
	freshState(t)
	api := newFake()
	press(t, loaded(t, api), "s", "f", "F", "u", "U", "p")
	if len(api.froze)+len(api.thawed) != 0 || len(savedSettings(t).Pins) != 0 {
		t.Fatalf("froze %v thawed %v pins %v", api.froze, api.thawed, savedSettings(t).Pins)
	}
}

func TestHelpNamesTheSettingsKey(t *testing.T) {
	freshState(t)
	if v := plainView(loaded(t, newFake())); !strings.Contains(v, "s settings") {
		t.Errorf("help:\n%s", v)
	}
}

func TestSettingsScreenFitsANarrowWindow(t *testing.T) {
	freshState(t)
	m := loaded(t, newFake())
	m = feed(t, m, tea.WindowSizeMsg{Width: 50, Height: 30})
	m = press(t, m, "s")
	for i, line := range strings.Split(plainView(m), "\n") {
		if n := len([]rune(line)); n > 50 {
			t.Errorf("line %d is %d wide, > 50: %q", i, n, line)
		}
	}
}
