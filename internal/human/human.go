// Package human formats sizes, paths and durations for display.
package human

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Bytes formats n with 1024-based units: "512B", "12K", "340M", "1.2G".
// K and M round down; G and above use one decimal.
func Bytes(n uint64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%dB", n)
	case n < 1<<20:
		return fmt.Sprintf("%dK", n>>10)
	case n < 1<<30:
		return fmt.Sprintf("%dM", n>>20)
	default:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	}
}

// Path replaces a leading $HOME with "~".
func Path(p string) string {
	home := os.Getenv("HOME")
	if home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+"/"); ok {
		return "~/" + rest
	}
	return p
}

// Duration formats d coarsely: "45s", "12m", "3h05m", "2d04h".
// Negative durations are treated as zero.
func Duration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int64(d / time.Second)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	case s < 86400:
		return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
	default:
		return fmt.Sprintf("%dd%02dh", s/86400, s%86400/3600)
	}
}
