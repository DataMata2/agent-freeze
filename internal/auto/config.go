//go:build linux || darwin

// Package auto freezes unused agents when memory or CPU runs short, and thaws one when its tab is selected.
package auto

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/DataMata2/agent-freeze/internal/freezer"
	"github.com/DataMata2/agent-freeze/internal/state"
)

// Config is the auto-mode settings file, shared by the TUI, the CLI and the service; see Settings.
type Config struct {
	Mode            string   `json:"mode"` // "manual" or "auto"
	IdleMinutes     float64  `json:"idle_minutes"`
	IntervalSeconds float64  `json:"interval_seconds"`
	MemoryLevel     string   `json:"memory_level"`
	CPULoad         float64  `json:"cpu_load"`
	CPUUsePercent   float64  `json:"cpu_use_percent"`
	AwayMinutes     float64  `json:"away_minutes"`
	RetryMinutes    float64  `json:"retry_minutes"`
	Notify          bool     `json:"notify"`
	TabThaw         bool     `json:"tab_thaw"`
	InputThaw       bool     `json:"input_thaw"`
	Pins            []string `json:"pins,omitempty"` // PinID of groups auto mode never freezes
}

// Defaults returns the settings used when the file is missing.
func Defaults() Config {
	return Config{Mode: "manual", IdleMinutes: 15, IntervalSeconds: 30, MemoryLevel: "warn", CPULoad: 1,
		CPUUsePercent: 10, AwayMinutes: 5, RetryMinutes: 30, Notify: true, TabThaw: true, InputThaw: true}
}

// DefaultPath returns auto.json next to the freeze state file.
func DefaultPath() string { return filepath.Join(filepath.Dir(state.DefaultPath()), "auto.json") }

// LoadConfig reads path over the defaults, clamped to each setting's range; a missing file yields the defaults.
func LoadConfig(path string) (Config, error) {
	c := Defaults()
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return Defaults(), fmt.Errorf("%s: %w", path, err)
	}
	if c.Mode != "auto" {
		c.Mode = "manual"
	}
	for _, st := range Settings {
		c = st.clamp(c)
	}
	return c, nil
}

// SaveConfig writes c to path atomically, creating the directory.
func SaveConfig(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(raw, '\n'))
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// PinID identifies g across restarts of its process.
func PinID(g freezer.GroupView) string {
	if g.SessionID != "" {
		return g.SessionID
	}
	return g.Key
}

// Pinned reports whether auto mode must leave g alone.
func (c Config) Pinned(g freezer.GroupView) bool { return slices.Contains(c.Pins, PinID(g)) }

// TogglePin returns a copy of c with g pinned, or unpinned if it was.
func (c Config) TogglePin(g freezer.GroupView) Config {
	id := PinID(g)
	pins := slices.DeleteFunc(slices.Clone(c.Pins), func(p string) bool { return p == id })
	if len(pins) == len(c.Pins) {
		pins = append(pins, id)
	}
	c.Pins = pins
	return c
}
