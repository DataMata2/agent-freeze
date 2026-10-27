package auto

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/DataMata2/agent-freeze/internal/freezer"
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
		"could not freeze Review: boom":             {Action: "froze", Name: "Review", Error: "boom"},
		"could not thaw Review: boom":               {Action: "thawed", Name: "Review", Error: "boom"},
		"froze Review (by hand, 1.0G)":              {Action: "froze", Name: "Review", By: ByManual, RSS: gib},
		"thawed Review (by hand)":                   {Action: "thawed", Name: "Review", By: ByManual},
	}
	for want, e := range cases {
		if got := e.String(); got != want {
			t.Errorf("String = %q, want %q", got, want)
		}
	}
}

func TestLogManualRecordsEveryResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto.log")
	results := []freezer.Result{
		{Name: "a", OK: true, RSSBefore: gib, RSSAfter: gib / 4},
		{Name: "b", Error: "not in user-delegated cgroup"},
	}
	if err := LogManual(path, false, results, now); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEvents(path, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("ReadEvents = %+v, %v", got, err)
	}
	b, a := got[0], got[1]
	if a.Name != "a" || a.Action != "froze" || a.By != ByManual || a.RSS != gib || a.Error != "" || !a.Time.Equal(now) {
		t.Errorf("first = %+v", a)
	}
	if b.Name != "b" || b.Action != "froze" || b.Error == "" {
		t.Errorf("second = %+v", b)
	}
}

func TestReadEventsGivesTheNewestFirstAcrossTheRotatedLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto.log")
	if got, err := ReadEvents(path, 5); err != nil || len(got) != 0 {
		t.Fatalf("ReadEvents on no log = %v, %v", got, err)
	}
	for i, name := range []string{"e1", "e2", "e3", "e4"} {
		target := path
		if i < 2 {
			target = path + ".1"
		}
		if err := AppendEvent(target, Event{Time: now.Add(time.Duration(i) * time.Minute), Action: "froze", Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, append(must(os.ReadFile(path)), []byte("not json\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEvents(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	if want := []string{"e4", "e3", "e2"}; !slices.Equal(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
