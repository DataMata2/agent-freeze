//go:build linux || darwin

package auto

import (
	"context"
	"errors"
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

// checkEvery returns the wait between checks; tests shorten it.
var checkEvery = func(cfg Config) time.Duration { return time.Duration(cfg.IntervalSeconds * float64(time.Second)) }

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

// InputWatch links Run to a watcher of typing in terminals; the zero value watches nothing.
type InputWatch struct {
	TTYs   chan []string // Run offers the ttys to watch; the watcher takes the newest
	Events <-chan string // a watched tty with unread input
}

// Runner performs auto-mode checks; every exported field is required.
type Runner struct {
	API    API
	Read   func() (pressure.Reading, error)
	CPU    func() map[int]float64 // recent CPU % per PID
	Notify func(title, body string)
	Now    func() time.Time
	Log    func(Event)
	// PendingInput returns how many typed bytes wait unread on tty; 0 if unknown.
	PendingInput func(tty string) int

	backoff map[string]time.Time // group key -> when its freeze last failed or was skipped
	frozeAt time.Time            // when auto mode last froze a group
	// futureSeen is when Check first saw a freeze time after its clock.
	futureSeen time.Time
	// autoKeys maps the key of each group auto mode froze, as last seen, to its tty;
	// nil until Run first looks.
	autoKeys map[string]string
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
	if !memoryShort(cfg, reading.Memory) && !cpuShort(cfg, reading.LoadPerCPU) {
		return nil
	}
	groups, err := r.API.List()
	if err != nil {
		return err
	}
	if r.coolingDown(cfg, groups, now) {
		return nil
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
	// Typed input the session has not read yet: the user is back.
	if g.TTY != "" && r.PendingInput(g.TTY) > 0 {
		r.backOff(g.Key, now) // or a tty that keeps unread bytes is picked at every check
		return nil
	}
	st, err := r.API.Status()
	if err != nil {
		return err
	}
	if st.SwapFree < g.RSS {
		r.backOff(g.Key, now)
		ev := Event{Time: now, Action: "skipped", Name: g.Name, RSS: g.RSS,
			Reason: fmt.Sprintf("not enough free swap: need %s, have %s", human.Bytes(g.RSS), human.Bytes(st.SwapFree))}
		if cfg.Notify {
			r.Notify(prog.Name, ev.String())
		}
		r.Log(ev)
		return nil
	}
	ev := Event{Time: now, Action: "froze", Name: g.Name, Reason: reason, RSS: g.RSS}
	if res := r.API.FreezeAs(g, false, frozenByAuto); !res.OK {
		ev.Action, ev.Error = "failed", res.Error
		r.backOff(g.Key, now)
	} else {
		r.frozeAt = now
		if r.autoKeys == nil {
			r.autoKeys = map[string]string{}
		}
		r.autoKeys[g.Key] = g.TTY
		if cfg.Notify {
			r.Notify(prog.Name, ev.String())
		}
	}
	r.Log(ev)
	return nil
}

// noteThaws restarts the idle time of each group auto mode froze that runs again
// although Run did not thaw it (u or afz thaw), as typing into it would.
// It lists only while such groups may exist.
func (r *Runner) noteThaws(focus Focus, now time.Time) error {
	if r.autoKeys != nil && len(r.autoKeys) == 0 {
		return nil
	}
	groups, err := r.API.List()
	if err != nil {
		return err
	}
	seen := map[string]string{}
	for _, g := range groups {
		if tty, ok := r.autoKeys[g.Key]; ok && tty != "" && g.State == "running" {
			focus.LeftAt[tty] = now
		}
		if autoFrozen(g) {
			seen[g.Key] = g.TTY
		}
	}
	r.autoKeys = seen
	return nil
}

// coolingDown reports whether the last auto freeze is too recent to judge
// pressure again. The freeze times in groups cover one made before a restart.
// A freeze time after now (the clock was set back) counts as when this
// runner first saw one, so it cannot stretch the cooldown.
func (r *Runner) coolingDown(cfg Config, groups []freezer.GroupView, now time.Time) bool {
	var last time.Time
	future := false
	see := func(at time.Time) {
		if at.After(now) {
			future = true
		} else if at.After(last) {
			last = at
		}
	}
	see(r.frozeAt)
	for _, g := range groups {
		if autoFrozen(g) && g.FrozenAt != nil {
			see(*g.FrozenAt)
		}
	}
	switch {
	case !future:
		r.futureSeen = time.Time{}
	case r.futureSeen.IsZero():
		r.futureSeen = now
	}
	if r.futureSeen.After(last) {
		last = r.futureSeen
	}
	wait := max(time.Duration(cooldownIntervals*cfg.IntervalSeconds*float64(time.Second)), minCooldown)
	return !last.IsZero() && now.Sub(last) < wait
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
	return r.thawTTY(tty, "tab selected")
}

// ThawInput thaws the groups auto mode froze whose terminal is tty, in auto mode with input thaw on.
func (r *Runner) ThawInput(cfg Config, tty string) error {
	if cfg.Mode != "auto" || !cfg.InputThaw || tty == "" {
		return nil
	}
	return r.thawTTY(tty, "input")
}

func (r *Runner) thawTTY(tty, reason string) error {
	groups, err := r.API.List()
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.TTY != tty || !autoFrozen(g) {
			continue
		}
		ev := Event{Time: r.Now(), Action: "thawed", Name: g.Name, Reason: reason}
		if res := r.API.Thaw(g); !res.OK {
			ev.Action, ev.Error = "failed", res.Error
		} else {
			delete(r.autoKeys, g.Key) // Run thawed it: not a thaw by hand
		}
		r.Log(ev)
	}
	return nil
}

// offerTTYs gives the input watcher the ttys of the groups auto mode froze, except those in skip,
// or none when input thaw is off. The watcher takes only the newest set.
func (r *Runner) offerTTYs(cfg Config, in InputWatch, skip map[string]bool) error {
	if in.TTYs == nil {
		return nil
	}
	var ttys []string
	if cfg.Mode == "auto" && cfg.InputThaw {
		groups, err := r.API.List()
		if err != nil {
			return err
		}
		for _, g := range groups {
			if autoFrozen(g) && g.TTY != "" && !skip[g.TTY] && !slices.Contains(ttys, g.TTY) {
				ttys = append(ttys, g.TTY)
			}
		}
	}
	for {
		select {
		case in.TTYs <- ttys:
			return nil
		default:
		}
		select { // drop the set the watcher has not taken yet
		case <-in.TTYs:
		default:
		}
	}
}

// Run checks every interval and thaws on each tab change and each input event until ctx ends,
// re-reading the settings each time.
func (r *Runner) Run(ctx context.Context, cfgPath string, tabs <-chan string, input InputWatch) error {
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
	// The ttys with input since the last check: left out of the offered sets until the
	// next check, so thaws that fail on two ttys do not take turns in a loop.
	tried := map[string]bool{}
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
		case tty, ok := <-input.Events:
			if !ok {
				input.Events = nil
				continue
			}
			// Typing counts as use: idle time starts again from now, as when a tab is left.
			focus.LeftAt[tty] = r.Now()
			cfg, err := LoadConfig(cfgPath)
			if err == nil {
				err = r.ThawInput(cfg, tty)
			}
			tried[tty] = true
			if err == nil {
				// A failed thaw is tried again after the next check, not in a loop.
				err = r.offerTTYs(cfg, input, tried)
			}
			report(err)
		case <-timer.C:
			clear(tried)
			cfg, err := LoadConfig(cfgPath)
			if err == nil {
				err = errors.Join(r.noteThaws(focus, r.Now()), r.Check(cfg, focus), r.offerTTYs(cfg, input, tried))
			}
			report(err)
			timer.Reset(checkEvery(cfg))
		}
	}
}
