package auto

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DataMata2/agent-freeze/internal/prog"
)

func fakeCommands(t *testing.T) (*[]string, string) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var calls []string
	old := runCmd
	runCmd = func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() { runCmd = old })
	return &calls, filepath.Join(cfg, "systemd", "user", unitName)
}

func TestInstallWritesTheUnitAndStartsIt(t *testing.T) {
	calls, unit := fakeCommands(t)
	if err := Install("/usr/local/bin/afz"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(unit)
	if err != nil || !strings.Contains(string(raw), `"/usr/local/bin/afz" auto run`) {
		t.Fatalf("unit %s: %v\n%s", unit, err, raw)
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable " + unitName,
		"systemctl --user restart " + unitName,
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s\nwant:\n%s", strings.Join(*calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestUninstallStopsAndRemovesTheUnit(t *testing.T) {
	calls, unit := fakeCommands(t)
	if err := Install("/usr/local/bin/afz"); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Fatalf("unit still there: %v", err)
	}
	want := "systemctl --user disable --now " + unitName + "\nsystemctl --user daemon-reload"
	if strings.Join(*calls, "\n") != want {
		t.Fatalf("commands = %v", *calls)
	}
	if err := Uninstall(); err != nil {
		t.Fatalf("second Uninstall: %v", err)
	}
}

func TestUnitPathIgnoresARelativeConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative")
	t.Setenv("HOME", "/home/u")
	if got := unitPath(); got != "/home/u/.config/systemd/user/"+unitName {
		t.Fatalf("unitPath = %q", got)
	}
}

// fakeNotifySend makes Notify find notify-send at path (or not at all when path
// is "") and records its stderr warnings; it never runs the real command.
func fakeNotifySend(t *testing.T, path string) (*int, *strings.Builder) {
	t.Helper()
	looks := 0
	var warn strings.Builder
	old := notifier
	notifier = &notifySend{
		look: func(file string) (string, error) {
			looks++
			if file != "notify-send" || path == "" {
				return "", errors.New("not found")
			}
			return path, nil
		},
		warn: &warn,
	}
	t.Cleanup(func() { notifier = old })
	return &looks, &warn
}

func TestNotifyNamesTheAppAndEscapesTitleAndBody(t *testing.T) {
	calls, _ := fakeCommands(t)
	looks, _ := fakeNotifySend(t, "/usr/bin/notify-send")
	Notify("a<z>", "froze <b>x</b> & y")
	Notify("afz", "again")
	want := []string{
		"/usr/bin/notify-send -a " + prog.Name + " -- a&lt;z&gt; froze &lt;b&gt;x&lt;/b&gt; &amp; y",
		"/usr/bin/notify-send -a " + prog.Name + " -- afz again",
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(*calls, "\n"), strings.Join(want, "\n"))
	}
	if *looks != 1 {
		t.Fatalf("looked up notify-send %d times, want once", *looks)
	}
}

func TestNotifyWithoutNotifySendWarnsOnceAndStopsTrying(t *testing.T) {
	calls, _ := fakeCommands(t)
	looks, warn := fakeNotifySend(t, "")
	Notify("afz", "one")
	Notify("afz", "two")
	if len(*calls) != 0 {
		t.Fatalf("ran %v without notify-send", *calls)
	}
	if *looks != 1 {
		t.Fatalf("looked up notify-send %d times, want once", *looks)
	}
	if got := warn.String(); strings.Count(got, "\n") != 1 || !strings.Contains(got, "notify-send") {
		t.Fatalf("warnings = %q, want one line naming notify-send", got)
	}
}
