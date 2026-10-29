//go:build linux || darwin

package freezer

import (
	"slices"
	"syscall"

	"github.com/DataMata2/agent-freeze/internal/banner"
	"github.com/DataMata2/agent-freeze/internal/discover"
	"github.com/DataMata2/agent-freeze/internal/procfs"
	"github.com/DataMata2/agent-freeze/internal/state"
)

// Banner and SIGWINCH hooks; tests replace them so that no real terminal
// is written and no real process is signalled.
var (
	showBanner  = banner.ShowFrozen
	clearBanner = banner.ClearFrozen
	sendWinch   = func(pid int) error { return syscall.Kill(pid, syscall.SIGWINCH) }
)

// bannerProc returns the process whose terminal shows the agent rooted at
// root: root itself, or for a claude pty host its lowest-PID claude child
// (the rule discover.Enrich uses). It returns nil if there is none.
func bannerProc(procs map[int]*procfs.Process, root *procfs.Process) *procfs.Process {
	if root == nil || !isPtyHost(root) {
		return root
	}
	var best *procfs.Process
	for _, p := range procs {
		if p.PPID == root.PID && discover.AgentOf(p) == "claude" && (best == nil || p.PID < best.PID) {
			best = p
		}
	}
	return best
}

// isPtyHost reports whether p is a claude background pty host, matching
// discover's rule.
func isPtyHost(p *procfs.Process) bool {
	return discover.AgentOf(p) == "claude" && slices.ContainsFunc(p.Args, func(a string) bool {
		return a == "bg-pty-host" || a == "--bg-pty-host"
	})
}

// owner returns who a freeze by by leaves as the owner of a group whose
// state entry was prev: a manual freeze ("") always takes over, and an auto
// freeze keeps the owner of a freeze that completed before (a partial
// group), so a group a person froze never becomes auto's.
func owner(prev state.Entry, by string) string {
	if by != "" && !prev.FrozenAt.IsZero() {
		return prev.By
	}
	return by
}

// claimManual marks the already frozen group key as frozen manually, so
// that a manual freeze of a group auto mode froze takes it over. It writes
// the state file only if the entry has an owner.
func claimManual(s state.Store, key string) error {
	m, err := s.Load()
	if err != nil {
		return err
	}
	if e, ok := m[key]; !ok || e.By == "" {
		return nil
	}
	return s.Update(func(m map[string]state.Entry) error {
		if e, ok := m[key]; ok {
			e.By = ""
			m[key] = e
		}
		return nil
	})
}
