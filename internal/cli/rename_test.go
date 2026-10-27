package cli

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installFake puts an "afz" file in a fresh PATH directory and makes it the
// running program.
func installFake(t *testing.T) (dir, bin string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "afz")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	oldArg0, oldExe := argv0, executable
	t.Cleanup(func() { argv0, executable = oldArg0, oldExe })
	argv0 = func() string { return "afz" }
	executable = func() (string, error) { return bin, nil }
	return dir, bin
}

func exists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Lstat(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func TestRename(t *testing.T) {
	dir, bin := installFake(t)
	code, out, errs := run(newFake(t), "rename", "chill")
	to := filepath.Join(dir, "chill")
	if code != 0 || errs != "" || out != "renamed afz to chill: "+to+"\n" {
		t.Fatalf("code=%d out=%q errs=%q", code, out, errs)
	}
	if !exists(t, to) || exists(t, bin) {
		t.Fatal("file not renamed")
	}
}

func TestRenameJSON(t *testing.T) {
	dir, bin := installFake(t)
	code, out, _ := run(newFake(t), "rename", "nap", "--json")
	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil || code != 0 {
		t.Fatalf("code=%d out=%q err=%v", code, out, err)
	}
	if got["from"] != bin || got["to"] != filepath.Join(dir, "nap") {
		t.Fatalf("got %v", got)
	}
}

func TestRenameRenamesTheLinkOnPath(t *testing.T) {
	_, real := installFake(t)
	bin := filepath.Join(t.TempDir(), "afz")
	if err := os.Symlink(real, bin); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin))
	if code, _, errs := run(newFake(t), "rename", "chill"); code != 0 {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
	target, err := os.Readlink(filepath.Join(filepath.Dir(bin), "chill"))
	if err != nil || target != real || !exists(t, real) {
		t.Fatalf("link -> %q (%v); the program itself must stay put", target, err)
	}
}

func TestRenameRefusals(t *testing.T) {
	dir, bin := installFake(t)
	if err := os.WriteFile(filepath.Join(dir, "git"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"rename"}, 2, "give exactly one new name"},
		{[]string{"rename", "a", "b"}, 2, "give exactly one new name"},
		{[]string{"rename", "../bin/x"}, 2, "not a valid command name"},
		{[]string{"rename", "a b"}, 2, "not a valid command name"},
		{[]string{"rename", "."}, 2, "not a valid command name"},
		{[]string{"rename", "--all", "x"}, 2, "not valid for rename"},
		{[]string{"rename", "afz"}, 2, "already called afz"},
		{[]string{"rename", "git"}, 1, "git is already a command"},
		{[]string{"rename", "notes"}, 1, "already exists"},
	} {
		code, out, errs := run(newFake(t), c.args...)
		if code != c.code || out != "" || !strings.Contains(errs, c.want) {
			t.Errorf("%v: code=%d out=%q errs=%q, want code %d and %q", c.args, code, out, errs, c.code, c.want)
		}
	}
	if !exists(t, bin) {
		t.Fatal("a refused rename moved the file")
	}
}

func TestRenameOnlyRenamesThisProgram(t *testing.T) {
	_, bin := installFake(t)
	executable = func() (string, error) { return filepath.Join(t.TempDir(), "afz"), nil }
	code, _, errs := run(newFake(t), "rename", "chill")
	if code != 1 || !strings.Contains(errs, "is not this program") || !exists(t, bin) {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
}
