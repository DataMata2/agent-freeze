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
	"github.com/DataMata2/agent-freeze/internal/pressure"
	"github.com/DataMata2/agent-freeze/internal/prog"
)

var stderr io.Writer = os.Stderr

// Runner performs auto-mode checks; every exported field is required.
type Runner struct {
	API    freezer.API
	Read   func() (pressure.Reading, error)
	CPU    func() map[int]float64 // recent CPU % per PID
	Notify func(title, body string)
	Now    func() time.Time
	Log    func(Event)

	failedAt map[string]time.Time // group key -> when its freeze last failed
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
	if !memoryShort(cfg, reading.Memory) && !cpuShort(cfg, reading.LoadPerCPU) {
		return nil
	}
	groups, err := r.API.List()
	if err != nil {
		return err
	}
	now := r.Now()
	groups = slices.DeleteFunc(groups, func(g freezer.GroupView) bool {
		at, failed := r.failedAt[g.Key]
		return failed && now.Sub(at).Minutes() < cfg.RetryMinutes
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
	ev := Event{Time: now, Action: "froze", By: ByAuto, Name: g.Name, Reason: reason, RSS: g.RSS}
	if res := r.API.Freeze(g, false); !res.OK {
		ev.Error = res.Error
		if r.failedAt == nil {
			r.failedAt = map[string]time.Time{}
		}
		r.failedAt[g.Key] = now
	} else if cfg.Notify {
		r.Notify(prog.Name, ev.String())
	}
	r.Log(ev)
	return nil
}

// ThawFocused thaws the frozen groups whose terminal is tty, in auto mode.
func (r *Runner) ThawFocused(cfg Config, tty string) error {
	if cfg.Mode != "auto" || !cfg.TabThaw || tty == "" || tty == FocusUnknown {
		return nil
	}
	groups, err := r.API.List()
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.TTY != tty || (g.State != "frozen" && g.State != "partial") {
			continue
		}
		ev := Event{Time: r.Now(), Action: "thawed", By: ByAuto, Name: g.Name, Reason: "tab selected"}
		if res := r.API.Thaw(g); !res.OK {
			ev.Error = res.Error
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
