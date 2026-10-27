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
		`"kind":"interactive","name":"afz overseer","nameSource":"user","status":"busy","statusUpdatedAt":1792800000000,"extra":1}`)
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
			Kind: "interactive", Name: "afz overseer", NameSource: "user", Status: "busy", StatusUpdatedAt: 1792800000000},
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
