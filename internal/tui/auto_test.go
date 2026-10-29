package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataMata2/agent-freeze/internal/auto"
)

func freshState(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func saveMode(t *testing.T, mode string) {
	t.Helper()
	cfg := auto.Defaults()
	cfg.Mode = mode
	if err := auto.SaveConfig(auto.DefaultPath(), cfg); err != nil {
		t.Fatal(err)
	}
}

func headerLine(m model) string { return strings.SplitN(plainView(m), "\n", 2)[0] }

func TestHeaderShowsTheMode(t *testing.T) {
	freshState(t)
	m := loaded(t, newFake())
	if !strings.Contains(headerLine(m), "mode manual") {
		t.Errorf("header: %q", headerLine(m))
	}
	saveMode(t, "auto")
	m = drain(t, m, m.refreshCmd())
	if !strings.Contains(headerLine(m), "mode auto") {
		t.Errorf("header: %q", headerLine(m))
	}
}

func TestMKeySwitchesTheMode(t *testing.T) {
	freshState(t)
	var calls []bool
	m := loaded(t, newFake())
	m.setAuto = func(on bool) error {
		calls = append(calls, on)
		if on {
			saveMode(t, "auto")
		} else {
			saveMode(t, "manual")
		}
		return nil
	}
	m = press(t, m, "m")
	if !slices.Equal(calls, []bool{true}) || !strings.Contains(m.msg, "auto mode on") || m.auto.Mode != "auto" {
		t.Fatalf("calls=%v msg=%q mode=%q", calls, m.msg, m.auto.Mode)
	}
	m = press(t, m, "m")
	if !slices.Equal(calls, []bool{true, false}) || !strings.Contains(m.msg, "auto mode off") {
		t.Fatalf("calls=%v msg=%q", calls, m.msg)
	}
}

func TestMKeyShowsWhyAutoModeDidNotStart(t *testing.T) {
	freshState(t)
	m := loaded(t, newFake())
	m.setAuto = func(bool) error { return errors.New("auto mode stays off: launchctl bootstrap: exit status 5") }
	m = press(t, m, "m")
	if !strings.Contains(m.msg, "exit status 5") {
		t.Fatalf("msg = %q", m.msg)
	}
}

func TestPKeyPinsAndUnpinsTheCurrentRow(t *testing.T) {
	freshState(t)
	m := loaded(t, newFake())
	m = press(t, m, "p")
	cfg, _ := auto.LoadConfig(auto.DefaultPath())
	if !slices.Equal(cfg.Pins, []string{"alpha-key"}) {
		t.Fatalf("pins = %v", cfg.Pins)
	}
	if !strings.Contains(plainView(m), "alpha [pinned]") || !strings.Contains(m.msg, "pinned alpha") {
		t.Errorf("msg=%q view:\n%s", m.msg, plainView(m))
	}
	m = press(t, m, "p")
	cfg, _ = auto.LoadConfig(auto.DefaultPath())
	if len(cfg.Pins) != 0 || strings.Contains(plainView(m), "[pinned]") {
		t.Fatalf("pins = %v", cfg.Pins)
	}
}

func TestViewShowsTheLastAutoAction(t *testing.T) {
	freshState(t)
	at := time.Date(2026, 10, 27, 15, 4, 0, 0, time.Local)
	if err := auto.AppendEvent(auto.LogPath(), auto.Event{Time: at, Action: "froze", Name: "api", Reason: "memory pressure warn", RSS: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	m := loaded(t, newFake())
	if !strings.Contains(plainView(m), "auto: Oct 27 15:04 froze api (memory pressure warn, 1.0G)") {
		t.Errorf("view:\n%s", plainView(m))
	}
}

func TestHelpNamesTheModeAndPinKeys(t *testing.T) {
	freshState(t)
	v := plainView(loaded(t, newFake()))
	if !strings.Contains(v, "m auto/manual") || !strings.Contains(v, "p pin") {
		t.Errorf("help:\n%s", v)
	}
}

func TestKeysWaitWhileTheModeSwitches(t *testing.T) {
	freshState(t)
	calls := 0
	m := loaded(t, newFake())
	m.setAuto = func(bool) error { calls++; return nil }
	nm, cmd := m.Update(key("m"))
	if cmd == nil || !nm.(model).busy {
		t.Fatal("m did not start a mode switch")
	}
	if _, again := nm.Update(key("m")); again != nil {
		t.Fatal("a second m ran while the first switch was running")
	}
	m = drain(t, nm.(model), cmd)
	if m.busy || calls != 1 {
		t.Fatalf("busy=%v calls=%d after the switch", m.busy, calls)
	}
}

func TestAutoKeysAreTextWhileTheFilterPromptIsOpen(t *testing.T) {
	freshState(t)
	m := loaded(t, newFake())
	called := false
	m.setAuto = func(bool) error { called = true; return nil }
	m = press(t, m, "/", "p", "m", "s")
	if !m.filtering || m.filter != "pms" {
		t.Fatalf("filtering=%v filter=%q, want prompt open with %q", m.filtering, m.filter, "pms")
	}
	cfg, _ := auto.LoadConfig(auto.DefaultPath())
	if called || m.settings || len(cfg.Pins) != 0 {
		t.Fatalf("prompt keys acted: setAuto called=%v settings=%v pins=%v", called, m.settings, cfg.Pins)
	}
}

func TestPKeyPinsTheCurrentFilteredRow(t *testing.T) {
	freshState(t)
	m := loaded(t, newFake())
	m = pressKeys(t, m, "/", "l", "t", "enter")
	if visibleKeys(m) != "delta-key" {
		t.Fatalf("visible = %q", visibleKeys(m))
	}
	m = press(t, m, "p")
	cfg, _ := auto.LoadConfig(auto.DefaultPath())
	if !slices.Equal(cfg.Pins, []string{"delta-key"}) {
		t.Fatalf("pins = %v, want [delta-key]", cfg.Pins)
	}
}
