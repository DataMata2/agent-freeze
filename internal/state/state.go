//go:build linux || darwin

// Package state persists freeze metadata in a JSON state file shared by all
// afz processes. Writers serialize through an flock on a sidecar lock file and
// replace the state file atomically; readers rely on the atomic rename and do
// not lock.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// Entry describes one frozen process group.
type Entry struct {
	Key       string         `json:"key"`
	Name      string         `json:"name"`
	Agent     string         `json:"agent"`
	Cwd       string         `json:"cwd"`
	Cgroup    string         `json:"cgroup"`
	FrozenAt  time.Time      `json:"frozen_at"`
	RSSBefore uint64         `json:"rss_before"`
	Freed     uint64         `json:"freed_bytes"`
	Origins   map[int]string `json:"origins"` // pid -> origin cgroup
	// By is "auto" when auto mode froze the group and "" for a manual freeze.
	By string `json:"by,omitempty"`

	// macOS only, where a freeze stops processes with signals.
	RootPID int            `json:"root_pid,omitempty"`
	Shell   int            `json:"shell,omitempty"`  // the job's shell, stopped with the group
	Starts  map[int]uint64 `json:"starts,omitempty"` // pid -> start time of every stopped process
}

// Store is a state file at Path.
type Store struct{ Path string }

const fileVersion = 1

type fileFormat struct {
	Version int              `json:"version"`
	Groups  map[string]Entry `json:"groups"`
}

// DefaultPath returns $XDG_STATE_HOME/agent-freeze/state.json when
// XDG_STATE_HOME is set and absolute, otherwise
// $HOME/.local/state/agent-freeze/state.json.
func DefaultPath() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "agent-freeze", "state.json")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state", "agent-freeze", "state.json")
}

// Load reads the state file. A missing file yields an empty map and nil error.
// It does not take the lock.
func (s Store) Load() (map[string]Entry, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state %s: %w", s.Path, err)
	}
	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse state %s (delete it to reset): %w", s.Path, err)
	}
	if f.Groups == nil {
		f.Groups = map[string]Entry{}
	}
	return f.Groups, nil
}

// Update takes an exclusive lock on Path+".lock", loads the state, calls fn
// and, if fn returns nil, saves the result atomically. If fn returns an error
// nothing is saved and that error is returned.
func (s Store) Update(fn func(m map[string]Entry) error) (err error) {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	lockPath := s.Path + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock %s: %w", lockPath, err)
	}
	defer lf.Close()
	if err := flock(lf, unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", lockPath, err)
	}
	defer func() {
		if uerr := flock(lf, unix.LOCK_UN); uerr != nil && err == nil {
			err = fmt.Errorf("unlock %s: %w", lockPath, uerr)
		}
	}()

	m, err := s.Load()
	if err != nil {
		return err
	}
	if err := fn(m); err != nil {
		return err
	}
	return s.save(m)
}

func flock(f *os.File, how int) error {
	for {
		err := unix.Flock(int(f.Fd()), how)
		if err != unix.EINTR {
			return err
		}
	}
}

// save writes m to a temp file, fsyncs it and renames it over Path.
// Callers must hold the lock.
func (s Store) save(m map[string]Entry) (err error) {
	if m == nil {
		m = map[string]Entry{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(fileFormat{Version: fileVersion, Groups: m}); err != nil {
		return fmt.Errorf("encode state: %w", err)
	}

	tmp := s.Path + ".tmp-" + strconv.Itoa(os.Getpid())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	// O_TRUNC on a pre-existing file keeps its old mode; enforce 0600.
	if err = f.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if _, err = f.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err = os.Rename(tmp, s.Path); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}
