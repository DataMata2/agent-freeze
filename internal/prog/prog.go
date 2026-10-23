// Package prog holds the name the command runs under, so every message and
// the banner use the new name after 'afz rename'.
package prog

import "path/filepath"

// Default is the name the command is installed with.
const Default = "afz"

// Name is the command's name; main sets it with FromArg0(os.Args[0]).
var Name = Default

// FromArg0 returns the base name of argv[0], or Default if it has none.
func FromArg0(arg0 string) string {
	if b := filepath.Base(arg0); b != "." && b != "/" {
		return b
	}
	return Default
}
