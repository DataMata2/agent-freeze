package auto

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEventsAppendAndReadBackTheLast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto.log")
	if _, ok := LastEvent(path); ok {
		t.Fatal("LastEvent on a missing log = ok")
	}
	first := Event{Time: now, Action: "froze", Name: "a", Reason: "memory pressure warn", RSS: gib}
	second := Event{Time: now.Add(time.Minute), Action: "thawed", Name: "b", Reason: "tab selected"}
	for _, e := range []Event{first, second} {
		if err := AppendEvent(path, e); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := LastEvent(path)
	if !ok || got.Name != "b" || !got.Time.Equal(second.Time) {
		t.Fatalf("LastEvent = %+v, %v", got, ok)
	}
}

func TestAppendEventStartsAFreshLogWhenItGrowsLarge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto.log")
	if err := os.WriteFile(path, make([]byte, maxLogBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendEvent(path, Event{Time: now, Action: "froze", Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Size() > 1000 {
		t.Fatalf("log is %d bytes after rotation", fi.Size())
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("old log not kept as .1: %v", err)
	}
}

func TestEventString(t *testing.T) {
	cases := map[string]Event{
		"froze Review (memory pressure warn, 1.0G)": {Action: "froze", Name: "Review", Reason: "memory pressure warn", RSS: gib},
		"thawed Review (tab selected)":              {Action: "thawed", Name: "Review", Reason: "tab selected"},
		"could not freeze Review: boom":             {Action: "failed", Name: "Review", Error: "boom"},
	}
	for want, e := range cases {
		if got := e.String(); got != want {
			t.Errorf("String = %q, want %q", got, want)
		}
	}
}
