package cli

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DataMata2/agent-freeze/internal/auto"
)

type fakeService struct {
	installed string
	removed   bool
	running   bool
	err       error
}

func useFakeService(t *testing.T) *fakeService {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := &fakeService{}
	oldI, oldU, oldR := installService, uninstallService, serviceRunning
	t.Cleanup(func() { installService, uninstallService, serviceRunning = oldI, oldU, oldR })
	installService = func(exe string) error {
		if s.err != nil {
			return s.err
		}
		s.installed, s.running = exe, true
		return nil
	}
	uninstallService = func() error { s.removed, s.running = true, false; return nil }
	serviceRunning = func() bool { return s.running }
	return s
}

func savedConfig(t *testing.T) auto.Config {
	t.Helper()
	c, err := auto.LoadConfig(auto.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAutoOnSavesAutoAndStartsTheServiceFromTheInstalledCommand(t *testing.T) {
	_, bin := installFake(t)
	s := useFakeService(t)
	code, out, errs := run(newFake(t), "auto", "on")
	if code != 0 || errs != "" {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if savedConfig(t).Mode != "auto" || s.installed != bin {
		t.Fatalf("mode=%q installed=%q", savedConfig(t).Mode, s.installed)
	}
	if !row(out, "mode", "auto") || !row(out, "service", "running") {
		t.Errorf("stdout:\n%s", out)
	}
}

func TestAutoOnStaysManualWhenTheServiceCannotStart(t *testing.T) {
	installFake(t)
	s := useFakeService(t)
	s.err = errors.New("launchctl bootstrap: exit status 5")
	code, _, errs := run(newFake(t), "auto", "on")
	if code != 1 || !strings.Contains(errs, "exit status 5") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if savedConfig(t).Mode != "manual" || !s.removed {
		t.Fatalf("mode=%q removed=%v, want manual and the half-installed service removed", savedConfig(t).Mode, s.removed)
	}
}

func TestAutoOffSavesManualAndRemovesTheService(t *testing.T) {
	installFake(t)
	s := useFakeService(t)
	run(newFake(t), "auto", "on")
	code, out, errs := run(newFake(t), "auto", "off")
	if code != 0 || errs != "" || !s.removed || savedConfig(t).Mode != "manual" {
		t.Fatalf("code=%d stderr=%q removed=%v mode=%q", code, errs, s.removed, savedConfig(t).Mode)
	}
	if !row(out, "mode", "manual") {
		t.Errorf("stdout:\n%s", out)
	}
}

func TestAutoSetChangesOneSetting(t *testing.T) {
	useFakeService(t)
	for _, args := range [][]string{{"idle", "0"}, {"memory", "critical"}, {"notify", "off"}, {"interval", "60"}} {
		if code, _, errs := run(newFake(t), append([]string{"auto", "set"}, args...)...); code != 0 || errs != "" {
			t.Fatalf("set %v: code=%d stderr=%q", args, code, errs)
		}
	}
	c := savedConfig(t)
	if c.IdleMinutes != 0 || c.MemoryLevel != "critical" || c.Notify || c.IntervalSeconds != 60 || c.Mode != "manual" {
		t.Fatalf("config = %+v", c)
	}
}

// row reports whether out has a line "<name>   <value>".
func row(out, name, value string) bool {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[0] == name && strings.Join(f[1:], " ") == value {
			return true
		}
	}
	return false
}

func TestAutoStatusShowsSettingsPinsAndTheLastEvent(t *testing.T) {
	s := useFakeService(t)
	s.running = true
	cfg := auto.Defaults()
	cfg.Mode, cfg.Pins = "auto", []string{"sess-1"}
	if err := auto.SaveConfig(auto.DefaultPath(), cfg); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 27, 15, 4, 0, 0, time.Local)
	ev := auto.Event{Time: at, Action: "froze", Name: "api", Reason: "memory pressure warn", RSS: 1 << 30}
	if err := auto.AppendEvent(auto.LogPath(), ev); err != nil {
		t.Fatal(err)
	}

	_, out, _ := run(newFake(t), "auto")
	for _, want := range [][2]string{{"mode", "auto"}, {"service", "running"}, {"idle", "15 min"}, {"interval", "30 s"},
		{"memory", "warn"}, {"cpu-load", "1 per core"}, {"cpu-use", "10 %"}, {"away", "5 min"}, {"retry", "30 min"},
		{"notify", "on"}, {"tab-thaw", "on"}, {"pinned", "1"},
		{"last", "Oct 27 15:04 froze api (memory pressure warn, 1.0G)"}} {
		if !row(out, want[0], want[1]) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}

	_, out, _ = run(newFake(t), "auto", "status", "--json")
	var got struct {
		Mode           string      `json:"mode"`
		IdleMinutes    float64     `json:"idle_minutes"`
		Pins           []string    `json:"pins"`
		ServiceRunning bool        `json:"service_running"`
		LastEvent      *auto.Event `json:"last_event"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got.Mode != "auto" || got.IdleMinutes != 15 || len(got.Pins) != 1 || !got.ServiceRunning || got.LastEvent == nil || got.LastEvent.Name != "api" {
		t.Errorf("json = %+v", got)
	}
}

func TestAutoUsageErrors(t *testing.T) {
	useFakeService(t)
	for _, args := range [][]string{
		{"auto", "frob"},
		{"auto", "idle", "5"},
		{"auto", "set"},
		{"auto", "set", "idle"},
		{"auto", "set", "nope", "1"},
		{"auto", "set", "idle", "-1"},
		{"auto", "set", "idle", "soon"},
		{"auto", "set", "memory", "high"},
		{"auto", "on", "now"},
		{"auto", "--all"},
	} {
		code, _, errs := run(newFake(t), args...)
		if code != 2 || !strings.Contains(errs, "usage:") {
			t.Errorf("%v: code=%d stderr=%q", args, code, errs)
		}
	}
	if savedConfig(t).IdleMinutes != 15 {
		t.Error("a usage error changed the settings")
	}
}

func TestAutoOnWaitsForTheServiceToStart(t *testing.T) {
	installFake(t)
	useFakeService(t)
	checks := 0
	serviceRunning = func() bool { checks++; return checks > 3 }
	_, out, _ := run(newFake(t), "auto", "on")
	if !row(out, "service", "running") {
		t.Errorf("stdout:\n%s", out)
	}
}

func TestRenameMovesTheRunningServiceToTheNewName(t *testing.T) {
	dir, _ := installFake(t)
	s := useFakeService(t)
	s.running = true
	if code, _, errs := run(newFake(t), "rename", "chill"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if want := filepath.Join(dir, "chill"); s.installed != want {
		t.Fatalf("service runs %q, want %q", s.installed, want)
	}
}

func TestRenameLeavesAStoppedServiceAlone(t *testing.T) {
	installFake(t)
	s := useFakeService(t)
	if code, _, _ := run(newFake(t), "rename", "chill"); code != 0 || s.installed != "" {
		t.Fatalf("code=%d installed=%q", code, s.installed)
	}
}

func TestFreezeAndThawByHandGoToTheHistory(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := newFake(t)
	if code, _, errs := run(f, "freeze", "102"); code != 0 || errs != "" {
		t.Fatalf("freeze: code=%d stderr=%q", code, errs)
	}
	if code, _, errs := run(f, "thaw", "201"); code != 0 || errs != "" {
		t.Fatalf("thaw: code=%d stderr=%q", code, errs)
	}
	got, err := auto.ReadEvents(auto.LogPath(), 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("history = %+v, %v", got, err)
	}
	if got[0].Action != "thawed" || got[0].Name != "claude back" || got[0].By != auto.ByManual {
		t.Errorf("newest = %+v", got[0])
	}
	if got[1].Action != "froze" || got[1].Name != "Acme web frontend builder" || got[1].By != auto.ByManual || got[1].RSS == 0 {
		t.Errorf("oldest = %+v", got[1])
	}
}
