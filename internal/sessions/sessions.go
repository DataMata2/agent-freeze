//go:build linux

// Package sessions reads the session metadata files Claude Code keeps in
// ~/.claude/sessions/<pid>.json. It only reads them, never writes.
package sessions

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Info is one session file.
type Info struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	Cwd        string `json:"cwd"`
	ProcStart  string `json:"procStart"` // /proc/<pid>/stat field 22, as a string
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	NameSource string `json:"nameSource"` // "user" or "derived"
	Status     string `json:"status"`     // "busy", "idle" or "shell"
}

// DefaultDir returns $CLAUDE_CONFIG_DIR/sessions if CLAUDE_CONFIG_DIR is set,
// else $HOME/.claude/sessions.
func DefaultDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "sessions")
	}
	return filepath.Join(os.Getenv("HOME"), ".claude", "sessions")
}

// Load reads every "<digits>.json" regular file in dir, keyed by the number
// in its name. Unreadable or unparsable files are skipped. A missing dir
// yields an empty map and no error.
func Load(dir string) (map[int]Info, error) {
	out := make(map[int]Info)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		stem, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !e.Type().IsRegular() || !allDigits(stem) {
			continue
		}
		pid, err := strconv.Atoi(stem)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var info Info
		if json.Unmarshal(data, &info) != nil {
			continue
		}
		out[pid] = info
	}
	return out, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
