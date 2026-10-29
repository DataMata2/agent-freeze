//go:build linux || darwin

package auto

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Setting is one auto-mode setting that the CLI and the TUI can change.
type Setting struct {
	Name, Label, Unit, Help string
	Min, Max, Inc           float64  // for numbers; Inc is one arrow-key step
	Choices                 []string // for words; on/off for switches
	num                     func(*Config) *float64
	flag                    func(*Config) *bool
	text                    func(*Config) *string
}

var onOff = []string{"on", "off"}

// Settings lists every auto-mode setting in display order.
var Settings = []Setting{
	{Name: "idle", Label: "idle time", Unit: "min", Help: "unused this long before a freeze",
		Max: 10080, Inc: 5, num: func(c *Config) *float64 { return &c.IdleMinutes }},
	{Name: "interval", Label: "check every", Unit: "s", Help: "how often memory and CPU are checked",
		Min: 5, Max: 3600, Inc: 5, num: func(c *Config) *float64 { return &c.IntervalSeconds }},
	{Name: "memory", Label: "memory trigger", Help: "memory pressure that starts freezing",
		Choices: []string{"warn", "critical", "off"}, text: func(c *Config) *string { return &c.MemoryLevel }},
	{Name: "cpu-load", Label: "CPU trigger", Unit: "per core", Help: "load per core that starts freezing; 0 = off",
		Max: 8, Inc: 0.25, num: func(c *Config) *float64 { return &c.CPULoad }},
	{Name: "cpu-use", Label: "CPU use to freeze", Unit: "%", Help: "of one core, used while unused",
		Max: 1000, Inc: 5, num: func(c *Config) *float64 { return &c.CPUUsePercent }},
	{Name: "away", Label: "away wait", Unit: "min", Help: "least unused time while the selected tab is unknown (macOS)",
		Max: 1440, Inc: 1, num: func(c *Config) *float64 { return &c.AwayMinutes }},
	{Name: "retry", Label: "retry after fail", Unit: "min", Help: "wait before trying a failed or skipped session again",
		Max: 1440, Inc: 5, num: func(c *Config) *float64 { return &c.RetryMinutes }},
	{Name: "notify", Label: "notifications", Help: "a notification on each auto freeze, and on the first skipped freeze under pressure",
		Choices: onOff, flag: func(c *Config) *bool { return &c.Notify }},
	{Name: "tab-thaw", Label: "thaw on tab", Help: "selecting an auto-frozen session's Terminal tab thaws it (macOS)",
		Choices: onOff, flag: func(c *Config) *bool { return &c.TabThaw }},
	{Name: "input-thaw", Label: "thaw on typing", Help: "typing into an auto-frozen session's terminal thaws it (Linux)",
		Choices: onOff, flag: func(c *Config) *bool { return &c.InputThaw }},
}

// Lookup returns the setting called name.
func Lookup(name string) (Setting, bool) {
	i := slices.IndexFunc(Settings, func(s Setting) bool { return s.Name == name })
	if i < 0 {
		return Setting{}, false
	}
	return Settings[i], true
}

// Value returns the setting's value in c as typed on the command line.
func (s Setting) Value(c Config) string {
	switch {
	case s.num != nil:
		return strconv.FormatFloat(*s.num(&c), 'f', -1, 64)
	case s.flag != nil && *s.flag(&c):
		return "on"
	case s.flag != nil:
		return "off"
	}
	return *s.text(&c)
}

// Display returns the value with its unit, e.g. "15 min".
func (s Setting) Display(c Config) string { return strings.TrimSpace(s.Value(c) + " " + s.Unit) }

// Set returns a copy of c with the setting set to v, or an error naming what is allowed.
func (s Setting) Set(c Config, v string) (Config, error) {
	if s.num != nil {
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || !(f >= s.Min && f <= s.Max) {
			return c, fmt.Errorf("%s: %q is not a number from %g to %g", s.Name, v, s.Min, s.Max)
		}
		*s.num(&c) = f
		return c, nil
	}
	v = strings.ToLower(strings.TrimSpace(v))
	if !slices.Contains(s.Choices, v) {
		return c, fmt.Errorf("%s: %q is not one of %s", s.Name, v, strings.Join(s.Choices, ", "))
	}
	if s.flag != nil {
		*s.flag(&c) = v == "on"
	} else {
		*s.text(&c) = v
	}
	return c, nil
}

// Step returns a copy of c with the setting moved one step up (dir 1) or down (dir -1).
func (s Setting) Step(c Config, dir int) Config {
	if s.num != nil {
		next := math.Round((*s.num(&c)+float64(dir)*s.Inc)/s.Inc) * s.Inc
		*s.num(&c) = min(max(next, s.Min), s.Max)
		return c
	}
	i := slices.Index(s.Choices, s.Value(c))
	c, _ = s.Set(c, s.Choices[(i+dir+len(s.Choices))%len(s.Choices)])
	return c
}

func (s Setting) clamp(c Config) Config {
	switch {
	case s.num != nil:
		*s.num(&c) = min(max(*s.num(&c), s.Min), s.Max)
	case s.text != nil && !slices.Contains(s.Choices, *s.text(&c)):
		*s.text(&c) = s.Value(Defaults())
	}
	return c
}
