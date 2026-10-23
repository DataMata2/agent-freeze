// Package discover groups a /proc snapshot into coding-agent process groups.
package discover

import (
	"cmp"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/DataMata2/agent-freeze/internal/procfs"
	"github.com/DataMata2/agent-freeze/internal/sessions"
)

// AgentNames are the executable names recognised as coding agents.
var AgentNames = []string{"claude", "codex", "gemini", "opencode", "aider", "cursor-agent", "amp"}

// interpreters run agents shipped as scripts; the script path names the agent.
var interpreters = []string{"node", "bun", "deno", "python", "python3"}

var scriptExts = []string{".js", ".mjs", ".cjs", ".ts", ".py"}

// Group is one agent process and the descendants that belong to it.
type Group struct {
	ID        string `json:"id"`  // strconv.Itoa(RootPID)
	Key       string `json:"key"` // fmt.Sprintf("%s-%d-%d", Agent, RootPID, StartTime)
	Agent     string `json:"agent"`
	Name      string `json:"name"`
	SessionID string `json:"session_id,omitempty"`
	Cwd       string `json:"cwd"`
	RootPID   int    `json:"root_pid"`
	StartTime uint64 `json:"start_time"`
	PIDs      []int  `json:"pids"` // ascending
	RSS       uint64 `json:"rss_bytes"`
	Swap      uint64 `json:"swap_bytes"`
	Daemon    bool   `json:"daemon"`
	Self      bool   `json:"self"`
	Status    string `json:"status,omitempty"` // "busy" | "idle" | "shell" | "" (unknown)
}

// AgentOf returns the agent name p runs, or "" if it is not an agent.
func AgentOf(p *procfs.Process) string {
	if p == nil {
		return ""
	}
	exeBase := base(p.Exe)
	argv0Base := ""
	if len(p.Args) > 0 {
		argv0Base = base(p.Args[0])
	}
	for _, name := range []string{exeBase, argv0Base, p.Comm} {
		if name != "" && slices.Contains(AgentNames, name) {
			return name
		}
	}
	if !slices.Contains(interpreters, exeBase) && !slices.Contains(interpreters, argv0Base) {
		return ""
	}
	script := ""
	for _, a := range p.Args[min(1, len(p.Args)):] {
		if !strings.HasPrefix(a, "-") {
			script = a
			break
		}
	}
	if script == "" {
		return ""
	}
	name := base(script)
	for _, ext := range scriptExts {
		if s, ok := strings.CutSuffix(name, ext); ok {
			name = s
			break
		}
	}
	if slices.Contains(AgentNames, name) {
		return name
	}
	return nodeModulesAgent(script)
}

// nodeModulesAgent matches "/node_modules/<agent>/" and
// "/node_modules/@<scope>/<agent>/" in a script path.
func nodeModulesAgent(p string) string {
	segs := strings.Split(p, "/")
	// Start at 1: the pattern needs a "/" before "node_modules".
	for i := 1; i < len(segs); i++ {
		if segs[i] != "node_modules" {
			continue
		}
		// The agent segment must be followed by another "/".
		if i+2 < len(segs) && slices.Contains(AgentNames, segs[i+1]) {
			return segs[i+1]
		}
		if i+3 < len(segs) && strings.HasPrefix(segs[i+1], "@") && slices.Contains(AgentNames, segs[i+2]) {
			return segs[i+2]
		}
	}
	return ""
}

func base(p string) string {
	if p == "" {
		return ""
	}
	return path.Base(p)
}

func hasArg(args []string, names ...string) bool {
	return slices.ContainsFunc(args, func(a string) bool { return slices.Contains(names, a) })
}

// isPtyHost reports whether p is a claude background pty host.
func isPtyHost(p *procfs.Process) bool {
	return AgentOf(p) == "claude" && hasArg(p.Args, "bg-pty-host", "--bg-pty-host")
}

// isDaemonProc reports whether p is the claude daemon itself.
func isDaemonProc(p *procfs.Process) bool {
	return AgentOf(p) == "claude" && len(p.Args) > 1 && p.Args[1] == "daemon"
}

// hostedArgs returns the command a pty host runs (the args after the first
// "--"), or p.Args for any other process.
func hostedArgs(p *procfs.Process) []string {
	if !isPtyHost(p) {
		return p.Args
	}
	if i := slices.Index(p.Args, "--"); i >= 0 {
		return p.Args[i+1:]
	}
	return nil
}

// index is the per-call view of a snapshot that the grouping logic shares.
type index struct {
	procs    map[int]*procfs.Process
	children map[int][]int // ppid -> pids, ascending
	agent    map[int]string
	roots    map[int]bool
}

func newIndex(procs map[int]*procfs.Process) *index {
	ix := &index{
		procs:    procs,
		children: make(map[int][]int),
		agent:    make(map[int]string),
		roots:    make(map[int]bool),
	}
	for pid, p := range procs {
		ix.children[p.PPID] = append(ix.children[p.PPID], pid)
		if a := AgentOf(p); a != "" {
			ix.agent[pid] = a
		}
	}
	for _, kids := range ix.children {
		slices.Sort(kids)
	}
	for pid := range ix.agent {
		anc := ix.nearestAgentAncestor(pid)
		if anc == 0 || isDaemonProc(procs[anc]) {
			ix.roots[pid] = true
		}
	}
	return ix
}

// nearestAgentAncestor walks the PPID chain of pid and returns the first
// agent process above it, or 0 if there is none before PID 1, a missing
// parent or a cycle.
func (ix *index) nearestAgentAncestor(pid int) int {
	seen := map[int]bool{pid: true}
	cur := ix.procs[pid].PPID
	for cur > 1 && !seen[cur] {
		p, ok := ix.procs[cur]
		if !ok {
			return 0
		}
		if ix.agent[cur] != "" {
			return cur
		}
		seen[cur] = true
		cur = p.PPID
	}
	return 0
}

// members returns root and its descendants, ascending, without descending
// into other roots and without selfPID.
func (ix *index) members(root, selfPID int) []int {
	if _, ok := ix.procs[root]; !ok {
		return nil
	}
	var out []int
	seen := map[int]bool{root: true}
	stack := []int{root}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if pid != selfPID {
			out = append(out, pid)
		}
		for _, c := range ix.children[pid] {
			if seen[c] || ix.roots[c] {
				continue
			}
			seen[c] = true
			stack = append(stack, c)
		}
	}
	slices.Sort(out)
	return out
}

// ancestry returns the PIDs on selfPID's PPID chain, not selfPID itself.
func (ix *index) ancestry(selfPID int) map[int]bool {
	out := make(map[int]bool)
	p, ok := ix.procs[selfPID]
	if !ok {
		return out
	}
	seen := map[int]bool{selfPID: true}
	for cur := p.PPID; cur > 0 && !seen[cur]; {
		seen[cur] = true
		out[cur] = true
		q, ok := ix.procs[cur]
		if !ok {
			break
		}
		cur = q.PPID
	}
	return out
}

// Members returns rootPID and its descendants in ascending order, stopping at
// descendants that are roots of their own groups and leaving out selfPID.
// It returns nil if rootPID is not in procs.
func Members(procs map[int]*procfs.Process, rootPID, selfPID int) []int {
	return newIndex(procs).members(rootPID, selfPID)
}

// Groups returns one Group per agent root, sorted by RSS descending, then
// RootPID ascending. A group is Self when one of its members is an ancestor
// of selfPID; since the root is always a member, that includes every group
// whose root selfPID descends from.
func Groups(procs map[int]*procfs.Process, selfPID int) []Group {
	ix := newIndex(procs)
	anc := ix.ancestry(selfPID)
	groups := make([]Group, 0, len(ix.roots))
	for root := range ix.roots {
		p := procs[root]
		agent := ix.agent[root]
		g := Group{
			ID:        strconv.Itoa(root),
			Key:       fmt.Sprintf("%s-%d-%d", agent, root, p.StartTime),
			Agent:     agent,
			RootPID:   root,
			StartTime: p.StartTime,
			PIDs:      ix.members(root, selfPID),
			Cwd:       p.Cwd,
		}
		for _, pid := range g.PIDs {
			g.RSS += procs[pid].RSS
			g.Swap += procs[pid].Swap
			if anc[pid] {
				g.Self = true
			}
		}
		if isPtyHost(p) {
			for _, c := range ix.children[root] {
				if ix.agent[c] != "" {
					g.Cwd = procs[c].Cwd
					break
				}
			}
		}
		args := hostedArgs(p)
		g.Daemon = isDaemonArgs(agent, args)
		g.SessionID = sessionID(args)
		g.Name = groupName(agent, args, g.Daemon, g.SessionID, g.Cwd)
		groups = append(groups, g)
	}
	slices.SortFunc(groups, func(a, b Group) int {
		if c := cmp.Compare(b.RSS, a.RSS); c != 0 {
			return c
		}
		return cmp.Compare(a.RootPID, b.RootPID)
	})
	return groups
}

func isDaemonArgs(agent string, args []string) bool {
	switch agent {
	case "claude":
		return (len(args) > 1 && args[1] == "daemon") ||
			hasArg(args, "bg-spare", "--bg-spare", "--chrome-native-host")
	case "codex":
		return hasArg(args, "app-server")
	}
	return false
}

// flagValue returns the value of the first occurrence of any of flags in
// args[1:], written either as "flag value" or "flag=value". In the
// space-separated form, a following arg that starts with "-" is not a value
// (a bare "--resume" opens the picker); that occurrence is skipped.
func flagValue(args []string, flags ...string) (string, bool) {
	for i := 1; i < len(args); i++ {
		a := args[i]
		for _, f := range flags {
			if a == f && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				return args[i+1], true
			}
			if v, ok := strings.CutPrefix(a, f+"="); ok {
				return v, true
			}
		}
	}
	return "", false
}

// sessionID takes --session-id, else --resume; a transcript path
// ("/dir/<id>.jsonl") is reduced to <id>.
func sessionID(args []string) string {
	v, ok := flagValue(args, "--session-id")
	if !ok {
		v, ok = flagValue(args, "--resume")
	}
	if !ok {
		return ""
	}
	if s, ok := strings.CutSuffix(v, ".jsonl"); ok {
		return path.Base(s)
	}
	return v
}

// collapse trims s and folds internal whitespace runs to one space.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// argvName returns the collapsed -n/--name value in args, or "".
func argvName(args []string) string {
	v, _ := flagValue(args, "-n", "--name")
	return collapse(v)
}

func groupName(agent string, args []string, daemon bool, session, cwd string) string {
	if n := argvName(args); n != "" {
		return n
	}
	if daemon {
		if hasArg(args, "bg-spare", "--bg-spare") {
			return agent + " bg-spare"
		}
		if hasArg(args, "--chrome-native-host") {
			return agent + " chrome-native-host"
		}
		for _, a := range args[min(1, len(args)):] {
			if !strings.HasPrefix(a, "-") {
				return collapse(agent + " " + a)
			}
		}
	}
	if n := collapse(session); n != "" {
		return n
	}
	if cwd == "" {
		return agent
	}
	return collapse(agent + " @ " + path.Base(cwd))
}

// Select returns the groups whose ID or Key equals sel; if there are none,
// the groups whose Name contains sel, ignoring case. An empty sel selects
// nothing.
func Select(groups []Group, sel string) []Group {
	if sel == "" {
		return nil
	}
	var exact, partial []Group
	low := strings.ToLower(sel)
	for _, g := range groups {
		if g.ID == sel || g.Key == sel {
			exact = append(exact, g)
		} else if strings.Contains(strings.ToLower(g.Name), low) {
			partial = append(partial, g)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return partial
}

// Enrich applies Claude session files (see package sessions) to the
// non-daemon claude groups in place: the session's name, ID and status.
// The session process is the group root, or for a pty host its lowest-PID
// claude child. A file whose ProcStart does not match the process's start
// time belongs to an earlier process with a reused PID and is ignored.
func Enrich(groups []Group, procs map[int]*procfs.Process, infos map[int]sessions.Info) {
	var children map[int][]int // built on first pty host
	for i := range groups {
		g := &groups[i]
		if g.Agent != "claude" || g.Daemon {
			continue
		}
		root, ok := procs[g.RootPID]
		if !ok {
			continue
		}
		pid := g.RootPID
		if isPtyHost(root) {
			if children == nil {
				children = make(map[int][]int)
				for cpid, p := range procs {
					children[p.PPID] = append(children[p.PPID], cpid)
				}
			}
			pid = 0
			for _, c := range children[g.RootPID] {
				if AgentOf(procs[c]) == "claude" && (pid == 0 || c < pid) {
					pid = c
				}
			}
			if pid == 0 {
				continue
			}
		}
		info, ok := infos[pid]
		if !ok {
			continue
		}
		p := procs[pid]
		if info.ProcStart != "" && !procfs.SameStart(p, info.ProcStart) {
			continue
		}
		name := collapse(info.Name)
		switch {
		case name == "":
		case info.NameSource == "user":
			g.Name = name
		case argvName(hostedArgs(p)) == "":
			g.Name = name
		}
		if info.SessionID != "" {
			g.SessionID = info.SessionID
		}
		g.Status = info.Status
	}
}
