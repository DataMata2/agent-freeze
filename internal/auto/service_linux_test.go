package auto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestNotifyEscapesMarkup(t *testing.T) {
	calls, _ := fakeCommands(t)
	Notify("afz", "froze <b>x</b> & y")
	if len(*calls) != 1 || (*calls)[0] != "notify-send afz froze &lt;b&gt;x&lt;/b&gt; &amp; y" {
		t.Fatalf("calls = %v", *calls)
	}
}
