//go:build linux || darwin

package auto

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// ServiceLabel names the background service on both systems.
const ServiceLabel = "io.github.datamata2.agent-freeze.auto"

const cmdTimeout = 10 * time.Second

var runCmd = func(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Run()
}

func errLogPath() string { return filepath.Join(filepath.Dir(DefaultPath()), "auto.err") }

// stateHome is the XDG_STATE_HOME the service needs to read the same settings as this process.
func stateHome() string { return filepath.Dir(filepath.Dir(DefaultPath())) }

func launchdPlist(exe, errLog, stateHome string) string {
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + ServiceLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + esc(exe) + `</string>
		<string>auto</string>
		<string>run</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardErrorPath</key>
	<string>` + esc(errLog) + `</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>XDG_STATE_HOME</key>
		<string>` + esc(stateHome) + `</string>
	</dict>
</dict>
</plist>
`
}

// systemdUnit is the user unit; env holds extra "NAME=value" settings.
// Environment= expands % specifiers but not $, so only % is doubled there.
func systemdUnit(exe, stateHome string, env ...string) string {
	quote := func(s string) string { return strings.ReplaceAll(strconv.Quote(s), "%", "%%") }
	var envLines strings.Builder
	for _, kv := range append([]string{"XDG_STATE_HOME=" + stateHome}, env...) {
		envLines.WriteString("Environment=" + quote(kv) + "\n")
	}
	return `[Unit]
Description=agent-freeze auto mode

[Service]
ExecStart=` + strings.ReplaceAll(quote(exe), "$", "$$") + ` auto run
` + envLines.String() + `Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
}

func focusLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == FocusUnknown || strings.HasPrefix(line, "/dev/") {
		return line, true
	}
	return "", false
}

// LockRun locks the auto loop so two never run at once, calling waiting and blocking while another holds it.
func LockRun(waiting func()) (*os.File, error) {
	path := filepath.Join(filepath.Dir(DefaultPath()), "auto.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		waiting()
		for err = unix.EINTR; errors.Is(err, unix.EINTR); {
			err = unix.Flock(int(f.Fd()), unix.LOCK_EX)
		}
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return f, nil
}

func writeServiceFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(errLogPath()), 0o700); err != nil {
		return err
	}
	return writeAtomic(path, []byte(content))
}
