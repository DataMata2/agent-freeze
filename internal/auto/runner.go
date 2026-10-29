//go:build linux || darwin

package auto

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/DataMata2/agent-freeze/internal/freezer"
	"github.com/DataMata2/agent-freeze/internal/human"
	"github.com/DataMata2/agent-freeze/internal/pressure"
	"github.com/DataMata2/agent-freeze/internal/prog"
)

var stderr io.Writer = os.Stderr

// After a freeze, Check waits cooldownIntervals check intervals, and at least
// minCooldown, so the freed memory shows in the next reading.
const (
	cooldownIntervals = 2
	minCooldown       = 60 * time.Second
)

// frozenByAuto is the freezer's "frozen by" mark of an auto-mode freeze.
const frozenByAuto = "auto"

// API is the freezer surface auto mode needs; *freezer.Freezer implements it.
type API interface {
	List() ([]freezer.GroupView, error)
	Status() (freezer.Status, error)
	Thaw(g freezer.GroupView) freezer.Result
	FreezeAs(g freezer.GroupView, forceSelf bool, by string) freezer.Result
}

var _ API = (*freezer.Freezer)(nil)

// Runner performs auto-mode checks; every exported field is required.
type Runner struct {
	API    API
	Read   func() (pressure.Reading, error)
	CPU    func() map[int]float64 // recent CPU % per PID
	Notify func(title, body string)
	Now    func() time.Time
	Log    func(Event)

	backoff map[string]time.Time // group key -> when its freeze last failed or was skipped
	frozeAt time.Time            // when auto mode last froze a group
}

// Check runs one auto-mode check and freezes at most one group.
func (r *Runner) Check(cfg Config, focus Focus) error {
	if cfg.Mode != "auto" {
		return nil
	}
	reading, err := r.Read()
	if err != nil {
		return err
	}
	perPID := r.CPU() // every check, so Linux deltas cover one interval
	now := r.Now()
	if r.coolingDown(cfg, now) {
		return nil
	}
	if !memoryShort(cfg, reading.Memory) && !cpuShort(cfg, reading.LoadPerCPU) {
		return nil
	}
	groups, err := r.API.List()
	if err != nil {
		return err
	}
	groups = slices.DeleteFunc(groups, func(g freezer.GroupView) bool {
		at, ok := r.backoff[g.Key]
		return ok && now.Sub(at).Minutes() < cfg.RetryMinutes
	})
	var cpu map[string]float64
	if cpuShort(cfg, reading.LoadPerCPU) {
		cpu = make(map[string]float64, len(groups))
		for _, g := range groups {
			cpu[g.Key] = pressure.GroupCPU(perPID, g.PIDs)
		}
	}
	g, reason, ok := Pick(groups, reading, cpu, cfg, focus, now)
	if !ok {
		return nil
	}
	// The list is a moment old: the session may have started a turn since.
	if g, ok, err = r.recheck(g.Key, cfg, focus, now); err != nil || !ok {
		return err
	}
	st, err := r.API.Status()
	if err != nil {
		return err
	}
	if st.SwapFree < g.RSS {
		r.backOff(g.Key, now)
		r.Log(Event{Time: now, Action: "skipped", Name: g.Name, RSS: g.RSS,
			Reason: fmt.Sprintf("not enough free swap: need %s, have %s", human.Bytes(g.RSS), human.Bytes(st.SwapFree))})
		return nil
	}
	ev := Event{Time: now, Action: "froze", Name: g.Name, Reason: reason, RSS: g.RSS}
	if res := r.API.FreezeAs(g, false, frozenByAuto); !res.OK {
		ev.Action, ev.Error = "failed", res.Error
		r.backOff(g.Key, now)
	} else {
		r.frozeAt = now
		if cfg.Notify {
			r.Notify(prog.Name, ev.String())
		}
	}
	r.Log(ev)
	return nil
}

// coolingDown reports whether the last freeze is too recent to judge pressure again.
func (r *Runner) coolingDown(cfg Config, now time.Time) bool {
	wait := max(time.Duration(cooldownIntervals*cfg.IntervalSeconds*float64(time.Second)), minCooldown)
	return !r.frozeAt.IsZero() && now.Sub(r.frozeAt) < wait
}

// recheck lists again and returns the group with key if it is still running and unused.
func (r *Runner) recheck(key string, cfg Config, focus Focus, now time.Time) (freezer.GroupView, bool, error) {
	groups, err := r.API.List()
	if err != nil {
		return freezer.GroupView{}, false, err
	}
	for _, g := range groups {
		if g.Key == key {
			return g, unused(g, cfg, focus, now), nil
		}
	}
	return freezer.GroupView{}, false, nil
}

// backOff keeps the group with key out of Check for cfg.RetryMinutes from now.
func (r *Runner) backOff(key string, now time.Time) {
	if r.backoff == nil {
		r.backoff = map[string]time.Time{}
	}
	r.backoff[key] = now
}

// autoFrozen reports whether g is frozen or partly frozen by auto mode, so auto mode may thaw it.
func autoFrozen(g freezer.GroupView) bool {
	return (g.State == "frozen" || g.State == "partial") && g.FrozenBy == frozenByAuto
}

// ThawFocused thaws the groups auto mode froze whose terminal is tty, in auto mode.
func (r *Runner) ThawFocused(cfg Config, tty string) error {
	if cfg.Mode != "auto" || !cfg.TabThaw || tty == "" || tty == FocusUnknown {
		return nil
	}
	groups, err := r.API.List()
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.TTY != tty || !autoFrozen(g) {
			continue
		}
		ev := Event{Time: r.Now(), Action: "thawed", Name: g.Name, Reason: "tab selected"}
		if res := r.API.Thaw(g); !res.OK {
			ev.Action, ev.Error = "failed", res.Error
		}
		r.Log(ev)
	}
	return nil
}

// Run checks every interval and thaws on each tab change until ctx ends, re-reading the settings each time.
func (r *Runner) Run(ctx context.Context, cfgPath string, tabs <-chan string) error {
	focus := Focus{TTY: FocusUnknown, LeftAt: map[string]time.Time{}}
	lastErr := ""
	report := func(err error) {
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if msg != "" && msg != lastErr {
			fmt.Fprintf(stderr, "%s auto: %s\n", prog.Name, msg)
		}
		lastErr = msg
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case tty, ok := <-tabs:
			if !ok {
				tabs, tty = nil, FocusUnknown
			}
			if focus.TTY != FocusUnknown && focus.TTY != tty {
				focus.LeftAt[focus.TTY] = r.Now()
			}
			focus.TTY = tty
			cfg, err := LoadConfig(cfgPath)
			if err == nil {
				err = r.ThawFocused(cfg, tty)
			}
			report(err)
		case <-timer.C:
			cfg, err := LoadConfig(cfgPath)
			if err == nil {
				err = r.Check(cfg, focus)
			}
			report(err)
			timer.Reset(time.Duration(cfg.IntervalSeconds * float64(time.Second)))
		}
	}
}
