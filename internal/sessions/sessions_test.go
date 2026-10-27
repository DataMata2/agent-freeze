package sessions_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/DataMata2/agent-freeze/internal/sessions"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "540032.json", `{"pid":540032,"sessionId":"0719","cwd":"/home/u/p","procStart":"11162181",`+
		`"kind":"interactive","name":"afz overseer","nameSource":"user","status":"busy","extra":1}`)
	write(t, dir, "77.json", `{"pid":77,"sessionId":"s77","name":"acme-api-e1","nameSource":"derived","status":"idle"}`)
	write(t, dir, "540032.json.key", `{"pid":1}`)
	write(t, dir, "78.key", `{"pid":78}`)
	write(t, dir, "79.json", `{not json`)
	write(t, dir, "abc.json", `{"pid":80}`)
	if err := os.Mkdir(filepath.Join(dir, "81.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := sessions.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[int]sessions.Info{
		540032: {PID: 540032, SessionID: "0719", Cwd: "/home/u/p", ProcStart: "11162181",
			Kind: "interactive", Name: "afz overseer", NameSource: "user", Status: "busy"},
		77: {PID: 77, SessionID: "s77", Name: "acme-api-e1", NameSource: "derived", Status: "idle"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load = %+v\nwant %+v", got, want)
	}
}

func TestLoadMissingDir(t *testing.T) {
	got, err := sessions.Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("Load = %#v, want empty non-nil map", got)
	}
}

func TestLoadNotADir(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	write(t, filepath.Dir(f), "file", "x")
	if _, err := sessions.Load(f); err == nil {
		t.Fatal("Load(file) err = nil, want error")
	}
}

func TestDefaultDir(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if got, want := sessions.DefaultDir(), "/home/u/.claude/sessions"; got != want {
		t.Errorf("DefaultDir() = %q, want %q", got, want)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/etc/cc")
	if got, want := sessions.DefaultDir(), "/etc/cc/sessions"; got != want {
		t.Errorf("DefaultDir() with CLAUDE_CONFIG_DIR = %q, want %q", got, want)
	}
}

func TestDirsAddsOtherClaudeConfigDirs(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{".claude/sessions", ".claude-work/sessions", ".claude-old"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	primary := filepath.Join(home, ".claude", "sessions")
	got := sessions.Dirs(primary)
	want := []string{primary, filepath.Join(home, ".claude-work", "sessions")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dirs = %v, want %v", got, want)
	}
}

func TestLoadAllMergesDirsFirstWins(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	write(t, a, "10.json", `{"pid":10,"name":"main account"}`)
	write(t, b, "10.json", `{"pid":10,"name":"stale twin"}`)
	write(t, b, "20.json", `{"pid":20,"name":"second account"}`)

	got := sessions.LoadAll([]string{a, filepath.Join(a, "nope"), b})
	want := map[int]sessions.Info{
		10: {PID: 10, Name: "main account"},
		20: {PID: 20, Name: "second account"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadAll = %+v\nwant %+v", got, want)
	}
}
