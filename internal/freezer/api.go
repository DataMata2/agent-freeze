// Package freezer freezes and thaws agent groups, with cgroups on Linux and
// signals on macOS. This file holds the types front ends depend on.
package freezer

import (
	"time"

	"github.com/DataMata2/agent-freeze/internal/discover"
)

// GroupView is a discovered agent group plus its freeze state.
type GroupView struct {
	discover.Group
	State     string     `json:"state"` // "running" | "frozen" | "partial"
	FrozenAt  *time.Time `json:"frozen_at,omitempty"`
	Freed     uint64     `json:"freed_bytes"`
	Delegated bool       `json:"delegated"`
}

// Result reports the outcome of freezing or thawing one group.
type Result struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Warning   string `json:"warning,omitempty"`
	RSSBefore uint64 `json:"rss_before"`
	RSSAfter  uint64 `json:"rss_after"`
	SwapAfter uint64 `json:"swap_after"`
}

// Status summarises frozen groups and system memory.
type Status struct {
	Frozen       int    `json:"frozen"`
	Freed        uint64 `json:"freed_bytes"`
	FrozenSwap   uint64 `json:"frozen_swap_bytes"`
	MemAvailable uint64 `json:"mem_available_bytes"`
	SwapFree     uint64 `json:"swap_free_bytes"`
}

// API is the freezer surface the CLI and TUI code against.
type API interface {
	// List returns all discovered groups with their freeze state.
	List() ([]GroupView, error)
	// Freeze freezes g; forceSelf allows freezing the caller's own group.
	Freeze(g GroupView, forceSelf bool) Result
	// Thaw thaws g.
	Thaw(g GroupView) Result
	// Status reports totals for frozen groups and system memory.
	Status() (Status, error)
}
