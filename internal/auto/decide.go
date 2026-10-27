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
	for _, g := range groups {
		if unused(g, cfg, focus, now) {
			cands = append(cands, g)
		}
	}
	if memoryShort(cfg, r.Memory) && len(cands) > 0 {
		slices.SortStableFunc(cands, func(a, b freezer.GroupView) int {
			if c := cmp.Compare(idleFor(b, focus, now), idleFor(a, focus, now)); c != 0 {
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
	switch {
	case g.State != "running", !g.Delegated, g.Self, g.Daemon, cfg.Pinned(g):
		return false
	case g.Status != "idle" && g.Status != "shell":
		return false
	case focus.TTY != "" && g.TTY == focus.TTY:
		return false
	}
	need := cfg.IdleMinutes
	// Without tab detection, a session being typed into must still never be frozen.
	if focus.TTY == FocusUnknown {
		need = max(need, cfg.AwayMinutes)
	}
	return idleFor(g, focus, now).Minutes() >= need
}

// idleFor is how long ago g last changed status or had its tab selected, 0 if unknown.
func idleFor(g freezer.GroupView, focus Focus, now time.Time) time.Duration {
	since := g.StatusSince
	if left, ok := focus.LeftAt[g.TTY]; ok && g.TTY != "" && (since == nil || left.After(*since)) {
		since = &left
	}
	if since == nil {
		return 0
	}
	return max(now.Sub(*since), 0)
}
