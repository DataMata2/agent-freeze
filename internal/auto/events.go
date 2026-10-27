//go:build linux || darwin

package auto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DataMata2/agent-freeze/internal/freezer"
	"github.com/DataMata2/agent-freeze/internal/human"
)

// maxLogBytes is the size from which AppendEvent moves the log to <path>.1.
const maxLogBytes = 512 << 10

// Who did an Event.
const (
	ByAuto   = "auto"
	ByManual = "manual"
)

// Event is one freeze or thaw, by auto mode or by hand; Error is set when it failed.
type Event struct {
	Time   time.Time `json:"time"`
	Action string    `json:"action"` // "froze" | "thawed"
	By     string    `json:"by,omitempty"`
	Name   string    `json:"name"`
	Reason string    `json:"reason,omitempty"`
	RSS    uint64    `json:"rss_bytes,omitempty"`
	Error  string    `json:"error,omitempty"`
}

func (e Event) String() string {
	if e.Failed() {
		return fmt.Sprintf("could not %s %s: %s", e.Verb(), e.Name, e.Error)
	}
	d := e.Details()
	if e.By == ByManual {
		d = strings.TrimSuffix("by hand, "+d, ", ")
	}
	if d != "" {
		return fmt.Sprintf("%s %s (%s)", e.Action, e.Name, d)
	}
	return e.Action + " " + e.Name
}

// Failed reports whether the freeze or thaw did not happen.
func (e Event) Failed() bool { return e.Error != "" }

// Verb is "thaw" or "freeze", for "could not ..." messages.
func (e Event) Verb() string {
	if e.Action == "thawed" {
		return "thaw"
	}
	return "freeze"
}

// Who is "you" for an action by hand, else "auto".
func (e Event) Who() string {
	if e.By == ByManual {
		return "you"
	}
	return ByAuto
}

// Details is why and how much, e.g. "memory pressure warn, 1.0G", or the error when it failed.
func (e Event) Details() string {
	if e.Failed() {
		return e.Error
	}
	var parts []string
	if e.Reason != "" {
		parts = append(parts, e.Reason)
	}
	if e.RSS > 0 {
		parts = append(parts, human.Bytes(e.RSS))
	}
	return strings.Join(parts, ", ")
}

// LogManual records freezes or thaws done by hand in the log at path.
func LogManual(path string, thaw bool, results []freezer.Result, now time.Time) error {
	action := "froze"
	if thaw {
		action = "thawed"
	}
	for _, r := range results {
		e := Event{Time: now, Action: action, By: ByManual, Name: r.Name, Error: r.Error}
		if !thaw && r.OK {
			e.RSS = r.RSSBefore
		}
		if err := AppendEvent(path, e); err != nil {
			return err
		}
	}
	return nil
}

// ReadEvents returns up to max events from the log at path and its rotated copy, newest first.
func ReadEvents(path string, max int) ([]Event, error) {
	var events []Event
	for _, p := range []string{path, path + ".1"} {
		raw, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
		for i := len(lines) - 1; i >= 0 && len(events) < max; i-- {
			var e Event
			if json.Unmarshal(lines[i], &e) == nil && e.Action != "" {
				events = append(events, e)
			}
		}
	}
	return events, nil
}

// LogPath returns auto.log next to the settings file.
func LogPath() string { return filepath.Join(filepath.Dir(DefaultPath()), "auto.log") }

// AppendEvent adds e to the JSON-lines log at path, rotating it to path.1 when full.
func AppendEvent(path string, e Event) error {
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogBytes {
		if err := os.Rename(path, path+".1"); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// LastEvent returns the newest event in the log at path.
func LastEvent(path string) (Event, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Event{}, false
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	var e Event
	if json.Unmarshal(lines[len(lines)-1], &e) != nil || e.Action == "" {
		return Event{}, false
	}
	return e, true
}
