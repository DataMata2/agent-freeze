package auto

import (
	"testing"
	"time"

	"github.com/DataMata2/agent-freeze/internal/discover"
	"github.com/DataMata2/agent-freeze/internal/freezer"
	"github.com/DataMata2/agent-freeze/internal/pressure"
)

var now = time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC)

const gib = 1 << 30

func idleGroup(name string, idleFor time.Duration, rss uint64) freezer.GroupView {
	since := now.Add(-idleFor)
	return freezer.GroupView{
		Group: discover.Group{
			ID: name, Key: name + "-key", SessionID: name + "-session", Agent: "claude", Name: name,
			RSS: rss, Status: "idle", StatusSince: &since, TTY: "/dev/" + name,
		},
		State: "running", Delegated: true,
	}
}

var (
	memWarn = pressure.Reading{Memory: pressure.Warn, LoadPerCPU: 0.3}
	calm    = pressure.Reading{Memory: pressure.Normal, LoadPerCPU: 0.3}
	cpuBusy = pressure.Reading{Memory: pressure.Normal, LoadPerCPU: 1.4}
	memCrit = pressure.Reading{Memory: pressure.Critical, LoadPerCPU: 0.3}
)

func pickName(t *testing.T, groups []freezer.GroupView, r pressure.Reading, cpu map[string]float64, cfg Config, focused string) string {
	t.Helper()
	g, reason, ok := Pick(groups, r, cpu, cfg, Focus{TTY: focused}, now)
	if !ok {
		return ""
	}
	if reason == "" {
		t.Errorf("Pick(%s) gave no reason", g.Name)
	}
	return g.Name
}

func TestPickFreezesNothingWhenTheMachineIsCalm(t *testing.T) {
	groups := []freezer.GroupView{idleGroup("a", 5*time.Hour, gib)}
	if got := pickName(t, groups, calm, nil, Defaults(), ""); got != "" {
		t.Fatalf("Pick = %q, want none", got)
	}
}

func TestPickUnderMemoryPressureTakesTheLongestUnusedThenTheBiggest(t *testing.T) {
	groups := []freezer.GroupView{
		idleGroup("recent", 20*time.Minute, 3*gib),
		idleGroup("old-small", 2*time.Hour, gib/2),
		idleGroup("old-big", 2*time.Hour, 2*gib),
	}
	if got := pickName(t, groups, memWarn, nil, Defaults(), ""); got != "old-big" {
		t.Fatalf("Pick = %q, want old-big", got)
	}
}

func TestPickSkipsGroupsItMustNotFreeze(t *testing.T) {
	busy := idleGroup("busy", time.Hour, gib)
	busy.Status = "busy"
	self := idleGroup("self", time.Hour, gib)
	self.Self = true
	daemon := idleGroup("daemon", time.Hour, gib)
	daemon.Daemon = true
	frozen := idleGroup("frozen", time.Hour, gib)
	frozen.State = "frozen"
	unknown := idleGroup("unknown", time.Hour, gib)
	unknown.Status = ""
	pinned := idleGroup("pinned", time.Hour, gib)
	focused := idleGroup("focused", time.Hour, gib)
	fresh := idleGroup("fresh", 5*time.Minute, gib)
	cfg := Defaults().TogglePin(pinned)
	groups := []freezer.GroupView{busy, self, daemon, frozen, unknown, pinned, focused, fresh}
	if got := pickName(t, groups, memWarn, nil, cfg, "/dev/focused"); got != "" {
		t.Fatalf("Pick = %q, want none", got)
	}
}

func TestPickTakesOnlyIdleSessions(t *testing.T) {
	// shell: a background task still runs; waiting: blocked on the user.
	for _, status := range []string{"shell", "waiting", "busy", ""} {
		g := idleGroup("g", time.Hour, gib)
		g.Status = status
		if got := pickName(t, []freezer.GroupView{g}, memWarn, nil, Defaults(), ""); got != "" {
			t.Errorf("status %q: Pick = %q, want none", status, got)
		}
	}
}

func TestPickWithZeroIdleMinutesTakesASessionWithoutTimestamp(t *testing.T) {
	g := idleGroup("no-time", time.Hour, gib)
	g.StatusSince = nil
	cfg := Defaults()
	if got := pickName(t, []freezer.GroupView{g}, memWarn, nil, cfg, ""); got != "" {
		t.Fatalf("with 15 idle minutes Pick = %q, want none", got)
	}
	cfg.IdleMinutes = 0
	if got := pickName(t, []freezer.GroupView{g}, memWarn, nil, cfg, ""); got != "no-time" {
		t.Fatalf("with 0 idle minutes Pick = %q, want no-time", got)
	}
}

func TestPickWhenTheCPUIsBusyTakesTheUnusedGroupBurningMostCPU(t *testing.T) {
	groups := []freezer.GroupView{
		idleGroup("quiet", 3*time.Hour, 3*gib),
		idleGroup("spinning", time.Hour, gib),
		idleGroup("warm", time.Hour, gib),
	}
	cpu := map[string]float64{"quiet-key": 0.5, "spinning-key": 85, "warm-key": 12}
	if got := pickName(t, groups, cpuBusy, cpu, Defaults(), ""); got != "spinning" {
		t.Fatalf("Pick = %q, want spinning", got)
	}
	cpu = map[string]float64{"quiet-key": 0.5, "spinning-key": 2, "warm-key": 3}
	if got := pickName(t, groups, cpuBusy, cpu, Defaults(), ""); got != "" {
		t.Fatalf("Pick = %q, want none when no unused group uses CPU", got)
	}
}

func TestPickWithUnknownFocusWaitsAtLeastFiveMinutes(t *testing.T) {
	cfg := Defaults()
	cfg.IdleMinutes = 0
	groups := []freezer.GroupView{idleGroup("typing", 2*time.Minute, gib)}
	if got := pickName(t, groups, memWarn, nil, cfg, FocusUnknown); got != "" {
		t.Fatalf("Pick = %q, want none while the tab in front is unknown", got)
	}
	groups = append(groups, idleGroup("left", 6*time.Minute, gib))
	if got := pickName(t, groups, memWarn, nil, cfg, FocusUnknown); got != "left" {
		t.Fatalf("Pick = %q, want left", got)
	}
}

func TestPickCountsATabYouJustLeftAsUsed(t *testing.T) {
	g := idleGroup("read", 3*time.Hour, gib)
	focus := Focus{TTY: "/dev/other", LeftAt: map[string]time.Time{"/dev/read": now.Add(-time.Minute)}}
	if got, _, ok := Pick([]freezer.GroupView{g}, memWarn, nil, Defaults(), focus, now); ok {
		t.Fatalf("Pick = %q, want none: its tab was selected a minute ago", got.Name)
	}
	focus.LeftAt["/dev/read"] = now.Add(-20 * time.Minute)
	if got, _, ok := Pick([]freezer.GroupView{g}, memWarn, nil, Defaults(), focus, now); !ok || got.Name != "read" {
		t.Fatalf("Pick = %q, %v, want read", got.Name, ok)
	}
}

func TestPickSkipsGroupsItCannotFreeze(t *testing.T) {
	g := idleGroup("foreign", 3*time.Hour, gib)
	g.Delegated = false
	if got := pickName(t, []freezer.GroupView{g}, memWarn, nil, Defaults(), ""); got != "" {
		t.Fatalf("Pick = %q, want none for a group outside the user's control", got)
	}
}

func TestPickWithAHugeIdleTimeFreezesNothing(t *testing.T) {
	cfg := Defaults()
	cfg.IdleMinutes = 1e300
	groups := []freezer.GroupView{idleGroup("old", 24*time.Hour, gib)}
	if got := pickName(t, groups, memWarn, nil, cfg, ""); got != "" {
		t.Fatalf("Pick = %q, want none", got)
	}
}

func TestPickFollowsTheMemoryTrigger(t *testing.T) {
	groups := []freezer.GroupView{idleGroup("old", 3*time.Hour, gib)}
	cfg := Defaults()
	cfg.MemoryLevel = "critical"
	if got := pickName(t, groups, memWarn, nil, cfg, ""); got != "" {
		t.Errorf("critical trigger froze %q at warn", got)
	}
	if got := pickName(t, groups, memCrit, nil, cfg, ""); got != "old" {
		t.Errorf("critical trigger at critical = %q, want old", got)
	}
	cfg.MemoryLevel = "off"
	if got := pickName(t, groups, memCrit, nil, cfg, ""); got != "" {
		t.Errorf("memory trigger off froze %q", got)
	}
}

func TestPickTakesTheCPUTriggerAndThresholdFromSettings(t *testing.T) {
	groups := []freezer.GroupView{idleGroup("spinning", time.Hour, gib)}
	cpu := map[string]float64{"spinning-key": 30}
	cfg := Defaults()
	cfg.CPULoad = 0
	if got := pickName(t, groups, cpuBusy, cpu, cfg, ""); got != "" {
		t.Errorf("CPU trigger off froze %q", got)
	}
	cfg.CPULoad = 2
	if got := pickName(t, groups, cpuBusy, cpu, cfg, ""); got != "" {
		t.Errorf("load 1.4 under a trigger of 2 froze %q", got)
	}
	cfg.CPULoad, cfg.CPUUsePercent = 1, 50
	if got := pickName(t, groups, cpuBusy, cpu, cfg, ""); got != "" {
		t.Errorf("30%% under a 50%% threshold froze %q", got)
	}
	cfg.CPUUsePercent = 25
	if got := pickName(t, groups, cpuBusy, cpu, cfg, ""); got != "spinning" {
		t.Errorf("30%% over a 25%% threshold = %q, want spinning", got)
	}
}

func TestPickTakesTheAwayWaitFromSettings(t *testing.T) {
	cfg := Defaults()
	cfg.IdleMinutes, cfg.AwayMinutes = 0, 1
	groups := []freezer.GroupView{idleGroup("left", 2*time.Minute, gib)}
	if got := pickName(t, groups, memWarn, nil, cfg, FocusUnknown); got != "left" {
		t.Fatalf("Pick = %q, want left after a 1-minute away wait", got)
	}
}
