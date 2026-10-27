package auto

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeCommands(t *testing.T) *[]string {
	t.Helper()
	return fakeCommandsFailing(t, 0)
}

// fakeCommandsFailing makes the first failures launchctl bootstrap calls fail.
func fakeCommandsFailing(t *testing.T, failures int) *[]string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	var calls []string
	old := runCmd
	runCmd = func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if len(args) > 0 && args[0] == "bootstrap" && failures > 0 {
			failures--
			return errors.New("Bootstrap failed: 5: Input/output error")
		}
		return nil
	}
	t.Cleanup(func() { runCmd = old })
	return &calls
}

func TestInstallWritesThePlistAndLoadsIt(t *testing.T) {
	calls := fakeCommands(t)
	if err := Install("/usr/local/bin/chill"); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", ServiceLabel+".plist")
	raw, err := os.ReadFile(plist)
	if err != nil || !strings.Contains(string(raw), "/usr/local/bin/chill") {
		t.Fatalf("plist %s: %v\n%s", plist, err, raw)
	}
	want := []string{
		"launchctl bootout gui/" + uid() + "/" + ServiceLabel,
		"launchctl bootstrap gui/" + uid() + " " + plist,
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s\nwant:\n%s", strings.Join(*calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestUninstallUnloadsAndRemovesThePlist(t *testing.T) {
	calls := fakeCommands(t)
	if err := Install("/usr/local/bin/chill"); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", ServiceLabel+".plist")
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatalf("plist still there: %v", err)
	}
	if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "launchctl bootout") {
		t.Fatalf("commands = %v", *calls)
	}
	if err := Uninstall(); err != nil {
		t.Fatalf("second Uninstall: %v", err)
	}
}

func TestInstallRetriesABootstrapThatRacesTheBootout(t *testing.T) {
	calls := fakeCommandsFailing(t, 2)
	if err := Install("/usr/local/bin/chill"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if n := strings.Count(strings.Join(*calls, "\n"), "bootstrap"); n != 3 {
		t.Fatalf("bootstrap ran %d times, want 3:\n%s", n, strings.Join(*calls, "\n"))
	}
}
