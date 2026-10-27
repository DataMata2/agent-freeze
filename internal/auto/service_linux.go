package auto

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/DataMata2/agent-freeze/internal/prog"
)

const unitName = "agent-freeze-auto.service"

func unitPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "systemd", "user", unitName)
}

// Install writes the systemd user unit that runs "exe auto run" at login and (re)starts it.
func Install(exe string) error {
	if err := writeServiceFile(unitPath(), systemdUnit(exe, stateHome())); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", unitName}, {"restart", unitName}} {
		if err := runCmd("systemctl", append([]string{"--user"}, args...)...); err != nil {
			return fmt.Errorf("systemctl --user %s: %w", args[0], err)
		}
	}
	return nil
}

// Uninstall stops the systemd user unit and removes it. Not installed is fine.
func Uninstall() error {
	_ = runCmd("systemctl", "--user", "disable", "--now", unitName)
	if err := os.Remove(unitPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := runCmd("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	return nil
}

// Running reports whether the systemd user unit is active.
func Running() bool { return runCmd("systemctl", "--user", "is-active", "--quiet", unitName) == nil }

// Notify shows a desktop notification with notify-send, whose body is markup.
func Notify(title, body string) {
	body = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(body)
	if err := runCmd("notify-send", title, body); err != nil {
		fmt.Fprintf(os.Stderr, "%s auto: notification: %s\n", prog.Name, err)
	}
}

// WatchFocus returns nil: there is no tab detection on Linux, so focus stays unknown.
func WatchFocus(context.Context) <-chan string { return nil }
