package auto

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

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

// notifySend finds notify-send once; tests replace notifier.
type notifySend struct {
	once sync.Once
	path string // "" when notify-send is missing
	look func(string) (string, error)
	warn io.Writer
}

var notifier = &notifySend{look: exec.LookPath, warn: os.Stderr}

// Notify shows a desktop notification with notify-send. Title and body are
// escaped as markup. Without notify-send it warns once and stops trying.
func Notify(title, body string) {
	n := notifier
	n.once.Do(func() {
		path, err := n.look("notify-send")
		if err != nil {
			fmt.Fprintf(n.warn, "%s auto: no notifications: notify-send not found: %s\n", prog.Name, err)
			return
		}
		n.path = path
	})
	if n.path == "" {
		return
	}
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	if err := runCmd(n.path, "-a", prog.Name, "--", esc.Replace(title), esc.Replace(body)); err != nil {
		fmt.Fprintf(n.warn, "%s auto: notification: %s\n", prog.Name, err)
	}
}

// WatchFocus returns nil: there is no tab detection on Linux, so focus stays unknown.
func WatchFocus(context.Context) <-chan string { return nil }
