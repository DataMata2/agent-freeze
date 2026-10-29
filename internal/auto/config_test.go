package auto

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DataMata2/agent-freeze/internal/discover"
	"github.com/DataMata2/agent-freeze/internal/freezer"
)

func TestLoadConfigMissingFileGivesDefaults(t *testing.T) {
	got, err := LoadConfig(filepath.Join(t.TempDir(), "auto.json"))
	if err != nil || !reflect.DeepEqual(got, Defaults()) {
		t.Fatalf("LoadConfig = %+v, %v; want %+v", got, err, Defaults())
	}
	if d := Defaults(); d.Mode != "manual" || d.IdleMinutes != 15 || d.IntervalSeconds != 30 || !d.Notify || !d.TabThaw || !d.InputThaw {
		t.Errorf("Defaults = %+v", d)
	}
}

func TestLoadConfigKeepsDefaultsForMissingKeysAndClampsBadValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto.json")
	if err := os.WriteFile(path, []byte(`{"mode":"turbo","idle_minutes":-3,"interval_seconds":1,"memory_level":"loud","cpu_load":-1,"away_minutes":1e9,"notify":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	want.IdleMinutes, want.IntervalSeconds, want.CPULoad, want.AwayMinutes, want.Notify = 0, 5, 0, 1440, false
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadConfig = %+v, want %+v", got, want)
	}
}

func TestLoadConfigRejectsBrokenJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto.json")
	if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("LoadConfig(broken) err = %v, want an error naming %s", err, path)
	}
}

func TestSaveConfigRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "auto.json")
	c := Defaults()
	c.Mode, c.IdleMinutes, c.IntervalSeconds, c.MemoryLevel, c.Notify, c.Pins = "auto", 0, 20, "critical", false, []string{"s1"}
	if err := SaveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("LoadConfig after Save = %+v, %v; want %+v", got, err, c)
	}
}

func group(key, session string) freezer.GroupView {
	return freezer.GroupView{Group: discover.Group{Key: key, SessionID: session}}
}

func TestPinsUseTheSessionIDElseTheKey(t *testing.T) {
	withSession, keyOnly := group("claude-1-2", "s1"), group("codex-3-4", "")
	c := Defaults().TogglePin(withSession).TogglePin(keyOnly)
	if !reflect.DeepEqual(c.Pins, []string{"s1", "codex-3-4"}) {
		t.Fatalf("Pins = %v", c.Pins)
	}
	if !c.Pinned(withSession) || !c.Pinned(keyOnly) || c.Pinned(group("x", "s2")) {
		t.Error("Pinned mismatch")
	}
	unpinned := c.TogglePin(withSession)
	if unpinned.Pinned(withSession) || !c.Pinned(withSession) {
		t.Error("TogglePin must unpin in the copy and leave the original pinned")
	}
}

func TestLoadConfigCapsTheInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto.json")
	if err := os.WriteFile(path, []byte(`{"interval_seconds":18446744074}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil || got.IntervalSeconds != 3600 {
		t.Fatalf("LoadConfig = %+v, %v; want interval 3600", got, err)
	}
}
