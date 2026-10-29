package auto

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/DataMata2/agent-freeze/internal/prog"
)

// JXA console.log writes to stderr; "?" means Terminal is not the app in front.
const focusScript = `const term = Application("Terminal");
let last = null, lastErr = null;
for (;;) {
  let tty = "?";
  try { if (term.running() && term.frontmost()) tty = term.windows[0].selectedTab.tty(); }
  catch (e) { tty = "?"; if (String(e) !== lastErr) { console.log(String(e)); lastErr = String(e); } }
  if (tty !== last) { console.log(tty); last = tty; }
  delay(1);
}`

const (
	focusRestartDelay = 5 * time.Second
	bootstrapTries    = 10
	bootstrapDelay    = 200 * time.Millisecond
)

func uid() string { return strconv.Itoa(os.Getuid()) }

func plistPath() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", ServiceLabel+".plist")
}

// Install writes the launchd agent that runs "exe auto run" at login and (re)starts it.
func Install(exe string) error {
	if err := writeServiceFile(plistPath(), launchdPlist(exe, errLogPath(), stateHome())); err != nil {
		return err
	}
	_ = runCmd("launchctl", "bootout", "gui/"+uid()+"/"+ServiceLabel)
	var err error
	for range bootstrapTries {
		// bootout returns before launchd has removed the old job, so bootstrap can fail at first.
		if err = runCmd("launchctl", "bootstrap", "gui/"+uid(), plistPath()); err == nil {
			return nil
		}
		time.Sleep(bootstrapDelay)
	}
	return fmt.Errorf("launchctl bootstrap: %w", err)
}

// Uninstall stops the launchd agent and removes it. Not installed is fine.
func Uninstall() error {
	_ = runCmd("launchctl", "bootout", "gui/"+uid()+"/"+ServiceLabel)
	if err := os.Remove(plistPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Running reports whether the launchd agent is running.
func Running() bool {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "launchctl", "print", "gui/"+uid()+"/"+ServiceLabel).Output()
	return err == nil && bytes.Contains(out, []byte("state = running"))
}

// Notify shows a macOS notification.
func Notify(title, body string) {
	err := runCmd("osascript", "-e", "on run argv", "-e",
		"display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s auto: notification: %s\n", prog.Name, err)
	}
}

// WatchFocus sends the tty of Terminal's selected tab on each change, or FocusUnknown.
func WatchFocus(ctx context.Context) <-chan string {
	ch := make(chan string)
	go func() {
		defer close(ch)
		for ctx.Err() == nil {
			watchFocusOnce(ctx, ch)
			select {
			case ch <- FocusUnknown:
			case <-ctx.Done():
				return
			}
			select {
			case <-time.After(focusRestartDelay):
			case <-ctx.Done():
			}
		}
	}()
	return ch
}

func watchFocusOnce(ctx context.Context, ch chan<- string) {
	cmd := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", focusScript)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	lines := bufio.NewScanner(stderr)
	for lines.Scan() {
		tty, ok := focusLine(lines.Text())
		if !ok {
			fmt.Fprintf(os.Stderr, "%s auto: reading the Terminal tab: %s\n", prog.Name, lines.Text())
			continue
		}
		select {
		case ch <- tty:
		case <-ctx.Done():
			return
		}
	}
}
