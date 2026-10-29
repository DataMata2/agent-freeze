//go:build linux || darwin

package auto

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/DataMata2/agent-freeze/internal/freezer"
	"github.com/DataMata2/agent-freeze/internal/pressure"
)

// FocusUnknown is the focused terminal when it cannot be read.
const FocusUnknown = "?"

// Focus is what auto mode knows about the selected Terminal tab.
type Focus struct {
	TTY    string               // the selected tab's terminal, or FocusUnknown
	LeftAt map[string]time.Time // when each terminal stopped being the selected tab
}

// Pick chooses at most one unused group to freeze, with the reason; cpu is % of a core by group key.
func Pick(groups []freezer.GroupView, r pressure.Reading, cpu map[string]float64, cfg Config,
	focus Focus, now time.Time) (freezer.GroupView, string, bool) {
	var cands []freezer.GroupView
	idle := make(map[string]time.Duration, len(groups)) // by key, so the sort stats nothing
	for _, g := range groups {
		if d, ok := unusedFor(g, cfg, focus, now); ok {
			cands = append(cands, g)
			idle[g.Key] = d
		}
	}
	if memoryShort(cfg, r.Memory) && len(cands) > 0 {
		slices.SortStableFunc(cands, func(a, b freezer.GroupView) int {
			if c := cmp.Compare(idle[b.Key], idle[a.Key]); c != 0 {
				return c
			}
			return cmp.Compare(b.RSS, a.RSS)
		})
		return cands[0], "memory pressure " + r.Memory.String(), true
	}
	if cpuShort(cfg, r.LoadPerCPU) {
		cands = slices.DeleteFunc(cands, func(g freezer.GroupView) bool { return cpu[g.Key] < cfg.CPUUsePercent })
		if len(cands) > 0 {
			slices.SortStableFunc(cands, func(a, b freezer.GroupView) int { return cmp.Compare(cpu[b.Key], cpu[a.Key]) })
			g := cands[0]
			return g, fmt.Sprintf("CPU busy, it used %.0f%% while unused", cpu[g.Key]), true
		}
	}
	return freezer.GroupView{}, "", false
}

func memoryShort(cfg Config, l pressure.Level) bool {
	switch cfg.MemoryLevel {
	case "warn":
		return l >= pressure.Warn
	case "critical":
		return l >= pressure.Critical
	}
	return false
}

func cpuShort(cfg Config, loadPerCPU float64) bool {
	return cfg.CPULoad > 0 && loadPerCPU >= cfg.CPULoad
}

func unused(g freezer.GroupView, cfg Config, focus Focus, now time.Time) bool {
	_, ok := unusedFor(g, cfg, focus, now)
	return ok
}

// unusedFor reports whether g may be frozen now, and how long it has been unused.
func unusedFor(g freezer.GroupView, cfg Config, focus Focus, now time.Time) (time.Duration, bool) {
	switch {
	case g.State != "running", !g.Delegated, g.Self, g.Daemon, cfg.Pinned(g):
		return 0, false
	case g.Status != "idle": // not "shell" (a background task runs) or "waiting" (blocked on you)
		return 0, false
	case focus.TTY != "" && g.TTY == focus.TTY:
		return 0, false
	}
	need := cfg.IdleMinutes
	// Without tab detection, a session being typed into must still never be frozen.
	// Where typing is measured (ttyActivity), it needs no extra wait.
	if focus.TTY == FocusUnknown && awayWaitApplies {
		need = max(need, cfg.AwayMinutes)
	}
	d := idleFor(g, focus, now)
	return d, d.Minutes() >= need
}

// ttyActivity reports when the agent last read input from tty; tests replace it.
var ttyActivity = ttyReadAt

// awayWaitApplies is whether AwayMinutes applies while the selected tab is unknown.
var awayWaitApplies = awayWaitDefault

// idleFor is how long ago g last changed status, had its tab selected or had
// typed input read from its terminal, 0 if unknown.
func idleFor(g freezer.GroupView, focus Focus, now time.Time) time.Duration {
	since := g.StatusSince
	if left, ok := focus.LeftAt[g.TTY]; ok && g.TTY != "" && (since == nil || left.After(*since)) {
		since = &left
	}
	if g.TTY != "" {
		if read, ok := ttyActivity(g.TTY); ok && (since == nil || read.After(*since)) {
			since = &read
		}
	}
	if since == nil {
		return 0
	}
	return max(now.Sub(*since), 0)
}
