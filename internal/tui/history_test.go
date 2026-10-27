package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DataMata2/agent-freeze/internal/auto"
)

func logEvents(t *testing.T, events ...auto.Event) {
	t.Helper()
	for _, e := range events {
		if err := auto.AppendEvent(auto.LogPath(), e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHKeyShowsWhoDidWhatAndWhy(t *testing.T) {
	freshState(t)
	at := time.Date(2026, 10, 27, 17, 0, 0, 0, time.Local)
	logEvents(t,
		auto.Event{Time: at, Action: "froze", By: auto.ByAuto, Name: "api", Reason: "memory pressure warn", RSS: 1 << 30},
		auto.Event{Time: at.Add(time.Minute), Action: "froze", By: auto.ByManual, Name: "web", RSS: 512 << 20},
		auto.Event{Time: at.Add(2 * time.Minute), Action: "thawed", By: auto.ByAuto, Name: "docs", Reason: "tab selected", Error: "boom"},
	)
	m := press(t, loaded(t, newFake()), "h")
	v := plainView(m)
	t.Logf("history at width 120:\n%s", v)
	for _, want := range []string{"History", "17:00", "auto", "you", "memory pressure warn", "1.0G", "could not thaw", "boom"} {
		if !strings.Contains(v, want) {
			t.Errorf("history lacks %q", want)
		}
	}
	if strings.Index(v, "docs") > strings.Index(v, "api") {
		t.Error("history is not newest first")
	}
	for _, back := range []string{"h", "esc"} {
		if v := plainView(press(t, loaded(t, newFake()), "h", back)); !strings.Contains(v, "NAME") {
			t.Errorf("%s did not go back to the sessions", back)
		}
	}
}

func TestFreezingInTheTUIGoesToTheHistory(t *testing.T) {
	freshState(t)
	m := press(t, loaded(t, newFake()), "f")
	got, err := auto.ReadEvents(auto.LogPath(), 5)
	if err != nil || len(got) != 1 || got[0].Name != "alpha" || got[0].By != auto.ByManual || got[0].Action != "froze" {
		t.Fatalf("history = %+v, %v (msg %q)", got, err, m.msg)
	}
}

func TestHistoryScrolls(t *testing.T) {
	freshState(t)
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.Local)
	for i := range 40 {
		logEvents(t, auto.Event{Time: at.Add(time.Duration(i) * time.Minute), Action: "froze", Name: fmt.Sprintf("s%02d", i)})
	}
	m := loaded(t, newFake())
	m = feed(t, m, tea.WindowSizeMsg{Width: 100, Height: 20})
	m = press(t, m, "h")
	if v := plainView(m); !strings.Contains(v, "s39") || strings.Contains(v, "s00") {
		t.Fatalf("first page should show the newest only:\n%s", v)
	}
	if n := len(strings.Split(plainView(m), "\n")); n > 20 {
		t.Errorf("history is %d lines tall in a 20-line window", n)
	}
	for range 40 {
		m = press(t, m, "j")
	}
	if v := plainView(m); !strings.Contains(v, "s00") {
		t.Fatalf("scrolling down never reached the oldest:\n%s", v)
	}
}

func TestEmptyHistorySaysSo(t *testing.T) {
	freshState(t)
	if v := plainView(press(t, loaded(t, newFake()), "h")); !strings.Contains(v, "nothing yet") {
		t.Errorf("empty history:\n%s", v)
	}
}

func TestHelpNamesTheHistoryKey(t *testing.T) {
	freshState(t)
	if v := plainView(loaded(t, newFake())); !strings.Contains(v, "h history") {
		t.Errorf("help:\n%s", v)
	}
}
