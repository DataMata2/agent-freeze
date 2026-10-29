//go:build darwin

package auto

import (
	"context"
	"time"
)

// WatchInput watches nothing: on macOS a session thaws when its tab is selected.
func WatchInput(context.Context) InputWatch { return InputWatch{} }

// PendingInput returns 0: macOS does not check for typed input.
func PendingInput(string) int { return 0 }

// awayWaitDefault is true: macOS cannot see typing, so AwayMinutes applies
// while the selected tab is unknown.
const awayWaitDefault = true

// ttyReadAt reports nothing: macOS uses the selected tab, not terminal reads.
func ttyReadAt(string) (time.Time, bool) { return time.Time{}, false }
