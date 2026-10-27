package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Seams for tests: the name the user typed and the running binary.
var (
	argv0      = func() string { return os.Args[0] }
	executable = os.Executable
)

var commandName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// rename renames the installed command to name in its own directory, so it
// is typed as name from then on. It refuses a name that is already a
// command or a file.
func rename(name string, o *output) error {
	if !commandName.MatchString(name) {
		return usagef("rename: %q is not a valid command name (letters, digits, '.', '_' and '-')", name)
	}
	from, err := installedPath()
	if err != nil {
		return err
	}
	to := filepath.Join(filepath.Dir(from), name)
	if to == from {
		return usagef("rename: the command is already called %s", name)
	}
	if p, err := exec.LookPath(name); err == nil {
		return fmt.Errorf("rename: %s is already a command (%s)", name, p)
	}
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("rename: %s already exists", to)
	}
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	if serviceRunning() {
		if err := installService(to); err != nil {
			return fmt.Errorf("rename: renamed to %s, but auto mode still runs the old name; run '%s auto on': %w", to, name, err)
		}
	}
	if o.json {
		o.writeJSON(map[string]string{"from": from, "to": to})
		return nil
	}
	fmt.Fprintf(o.stdout, "renamed %s to %s: %s\n", filepath.Base(from), name, to)
	return nil
}

// installedPath returns the file the user ran: argv[0], looked up in PATH
// when it has no slash. That file may be a symlink to the binary; renaming
// the link renames the command and leaves the binary alone.
func installedPath() (string, error) {
	p := argv0()
	if !strings.Contains(p, "/") {
		found, err := exec.LookPath(p)
		if err != nil {
			return "", fmt.Errorf("rename: cannot find %s in PATH: %w", p, err)
		}
		p = found
	}
	p, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("rename: %w", err)
	}
	self, err := executable()
	if err != nil {
		return "", fmt.Errorf("rename: %w", err)
	}
	if resolve(p) != resolve(self) {
		return "", fmt.Errorf("rename: %s is not this program; rename the file yourself", p)
	}
	return p, nil
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
