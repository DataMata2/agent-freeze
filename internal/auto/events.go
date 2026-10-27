//go:build linux || darwin

package auto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/DataMata2/agent-freeze/internal/human"
)

// maxLogBytes is the size from which AppendEvent moves the log to <path>.1.
const maxLogBytes = 512 << 10

// Event is one thing auto mode did.
type Event struct {
	Time   time.Time `json:"time"`
	Action string    `json:"action"` // "froze" | "thawed" | "failed"
	Name   string    `json:"name"`
	Reason string    `json:"reason,omitempty"`
	RSS    uint64    `json:"rss_bytes,omitempty"`
	Error  string    `json:"error,omitempty"`
}

func (e Event) String() string {
	switch e.Action {
	case "failed":
		return fmt.Sprintf("could not freeze %s: %s", e.Name, e.Error)
	case "froze":
		return fmt.Sprintf("froze %s (%s, %s)", e.Name, e.Reason, human.Bytes(e.RSS))
	}
	return fmt.Sprintf("%s %s (%s)", e.Action, e.Name, e.Reason)
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
