//go:build darwin

package auto

import "context"

// WatchInput watches nothing: on macOS a session thaws when its tab is selected.
func WatchInput(context.Context) InputWatch { return InputWatch{} }

// PendingInput returns 0: macOS does not check for typed input.
func PendingInput(string) int { return 0 }
