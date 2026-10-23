//go:build linux || darwin

package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T) Store {
	t.Helper()
	return Store{Path: filepath.Join(t.TempDir(), "sub", "state.json")}
}

func TestLoadMissing(t *testing.T) {
	s := newStore(t)
	m, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m == nil || len(m) != 0 {
		t.Fatalf("want empty non-nil map, got %#v", m)
	}
}

func TestLoadNullGroups(t *testing.T) {
	for _, content := range []string{`{"version":1,"groups":null}`, `{"version":1}`, `{"version":7,"groups":{}}`} {
		dir := t.TempDir()
		p := filepath.Join(dir, "state.json")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		m, err := Store{Path: p}.Load()
		if err != nil {
			t.Fatalf("%s: Load: %v", content, err)
		}
		if m == nil || len(m) != 0 {
			t.Fatalf("%s: want empty non-nil map, got %#v", content, m)
		}
	}
}

func TestUpdateRoundTrip(t *testing.T) {
	s := newStore(t)
	frozen := time.Date(2026, 10, 21, 12, 34, 56, 789, time.UTC)
	want := Entry{
		Key:       "k1",
		Name:      "claude",
		Agent:     "claude",
		Cwd:       "/home/x/proj",
		Cgroup:    "/sys/fs/cgroup/afz/k1",
		FrozenAt:  frozen,
		RSSBefore: 123456789,
		Freed:     98765,
		Origins:   map[int]string{12: "/a", 13: "/b"},
	}
	if err := s.Update(func(m map[string]Entry) error {
		m[want.Key] = want
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	m, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := m["k1"]
	if !ok || len(m) != 1 {
		t.Fatalf("want one entry k1, got %#v", m)
	}
	if !got.FrozenAt.Equal(frozen) {
		t.Errorf("FrozenAt = %v, want %v", got.FrozenAt, frozen)
	}
	if got.Key != want.Key || got.Name != want.Name || got.Agent != want.Agent ||
		got.Cwd != want.Cwd || got.Cgroup != want.Cgroup ||
		got.RSSBefore != want.RSSBefore || got.Freed != want.Freed {
		t.Errorf("entry mismatch: got %#v want %#v", got, want)
	}
	if len(got.Origins) != 2 || got.Origins[12] != "/a" || got.Origins[13] != "/b" {
		t.Errorf("Origins = %#v", got.Origins)
	}

	raw, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	str := string(raw)
	for _, sub := range []string{`"version": 1`, `"groups": {`, `"freed_bytes": 98765`, `"rss_before": 123456789`, `"frozen_at": `, `"origins": {`, `"12": "/a"`, "\n  \""} {
		if !strings.Contains(str, sub) {
			t.Errorf("file missing %q:\n%s", sub, str)
		}
	}
	if !strings.HasSuffix(str, "\n") {
		t.Errorf("file does not end with newline")
	}
	fi, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(s.Path))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
	}
	// No temp files left behind.
	ents, _ := os.ReadDir(filepath.Dir(s.Path))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestUpdateFnErrorDoesNotSave(t *testing.T) {
	s := newStore(t)
	if err := s.Update(func(m map[string]Entry) error {
		m["a"] = Entry{Key: "a"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("boom")
	err := s.Update(func(m map[string]Entry) error {
		m["b"] = Entry{Key: "b"}
		delete(m, "a")
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Update err = %v, want sentinel", err)
	}
	m, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["a"]; !ok || len(m) != 1 {
		t.Fatalf("state changed after fn error: %#v", m)
	}
}

func TestUpdateFnErrorOnMissingFileCreatesNothing(t *testing.T) {
	s := newStore(t)
	sentinel := errors.New("boom")
	if err := s.Update(func(m map[string]Entry) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatalf("state file exists after fn error: %v", err)
	}
}

func TestLoadCorrupt(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load()
	if err == nil {
		t.Fatal("want error for corrupt file")
	}
	if !strings.Contains(err.Error(), s.Path) {
		t.Errorf("error %q does not mention path %q", err, s.Path)
	}
	// Update must also fail and not overwrite the corrupt file.
	if err := s.Update(func(m map[string]Entry) error { return nil }); err == nil {
		t.Error("Update on corrupt file: want error")
	}
	raw, _ := os.ReadFile(s.Path)
	if string(raw) != "{not json" {
		t.Errorf("corrupt file was overwritten: %q", raw)
	}
}

func TestDefaultPath(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv("XDG_STATE_HOME", xdg)
	if got, want := DefaultPath(), filepath.Join(xdg, "agent-freeze", "state.json"); got != want {
		t.Errorf("abs XDG: got %q want %q", got, want)
	}

	t.Setenv("XDG_STATE_HOME", "relative/dir")
	if got, want := DefaultPath(), filepath.Join(home, ".local", "state", "agent-freeze", "state.json"); got != want {
		t.Errorf("relative XDG: got %q want %q", got, want)
	}

	t.Setenv("XDG_STATE_HOME", "")
	if got, want := DefaultPath(), filepath.Join(home, ".local", "state", "agent-freeze", "state.json"); got != want {
		t.Errorf("empty XDG: got %q want %q", got, want)
	}
}

func TestConcurrentUpdates(t *testing.T) {
	s := newStore(t)
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("g%d", i)
			errs <- s.Update(func(m map[string]Entry) error {
				m[key] = Entry{Key: key}
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	m, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != n {
		t.Fatalf("got %d keys, want %d: %v", len(m), n, m)
	}
	for i := 0; i < n; i++ {
		if _, ok := m[fmt.Sprintf("g%d", i)]; !ok {
			t.Errorf("missing key g%d", i)
		}
	}
}

// TestStateWorker is the entry point for the cross-process test. It does
// nothing unless AFZ_STATE_WORKER is set.
func TestStateWorker(t *testing.T) {
	id := os.Getenv("AFZ_STATE_WORKER")
	if id == "" {
		return
	}
	path := os.Getenv("AFZ_STATE_PATH")
	if path == "" {
		t.Fatal("AFZ_STATE_PATH not set")
	}
	s := Store{Path: path}
	for j := 0; j < 10; j++ {
		key := fmt.Sprintf("w%s-%d", id, j)
		if err := s.Update(func(m map[string]Entry) error {
			m[key] = Entry{Key: key}
			return nil
		}); err != nil {
			t.Fatalf("Update %s: %v", key, err)
		}
	}
}

func TestConcurrentUpdatesCrossProcess(t *testing.T) {
	if os.Getenv("AFZ_STATE_WORKER") != "" {
		t.Skip("inside worker")
	}
	s := newStore(t)
	const workers = 5
	// Bound the workers so a lock deadlock fails the test instead of hanging.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmds := make([]*exec.Cmd, workers)
	outs := make([]*strings.Builder, workers)
	for i := 0; i < workers; i++ {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStateWorker$", "-test.timeout=30s")
		cmd.Env = append(os.Environ(),
			"AFZ_STATE_WORKER="+strconv.Itoa(i),
			"AFZ_STATE_PATH="+s.Path,
		)
		out := &strings.Builder{}
		cmd.Stdout = out
		cmd.Stderr = out
		if err := cmd.Start(); err != nil {
			t.Fatalf("start worker %d: %v", i, err)
		}
		cmds[i] = cmd
		outs[i] = out
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Errorf("worker %d: %v\n%s", i, err, outs[i])
		}
	}
	m, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != workers*10 {
		t.Fatalf("got %d keys, want %d", len(m), workers*10)
	}
	for i := 0; i < workers; i++ {
		for j := 0; j < 10; j++ {
			if _, ok := m[fmt.Sprintf("w%d-%d", i, j)]; !ok {
				t.Errorf("missing key w%d-%d", i, j)
			}
		}
	}
}
