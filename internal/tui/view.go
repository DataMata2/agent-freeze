//go:build linux

package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/DataMata2/agent-freeze/internal/freezer"
	"github.com/DataMata2/agent-freeze/internal/human"
)

const defaultWidth = 100

// Fixed column widths. The name and cwd columns share what is left.
const (
	prefixW = 6 // "> [x] "
	stateW  = 7 // "running" / "partial"
	statusW = 6 // "busy" / "idle" / "shell" / "-"
	ramW    = 6
	swapW   = 6
	procsW  = 5
	// prefix + name + " state status ram swap procs " + cwd
	fixedW   = prefixW + 1 + stateW + 1 + statusW + 1 + ramW + 1 + swapW + 1 + procsW + 1
	maxNameW = 30
	minNameW = 8
	minCwdW  = 10
)

const helpText = "↑/↓ j/k move  space select  a all  f freeze  u thaw  F freeze all  U thaw all  r refresh  q quit"

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var (
	titleStyle   = lipgloss.NewStyle().Bold(true)
	colHeadStyle = lipgloss.NewStyle().Bold(true).Faint(true)
	helpStyle    = lipgloss.NewStyle().Faint(true)
	promptStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	errStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	busyFg       = lipgloss.Color("5")
	partialFg    = lipgloss.Color("3")
)

func (m model) View() string {
	w := m.width
	if w <= 0 {
		w = defaultWidth
	}
	var b strings.Builder
	line := func(s string, st lipgloss.Style) {
		b.WriteString(st.Render(truncRight(s, w)))
		b.WriteByte('\n')
	}

	line(m.header(), titleStyle)
	b.WriteByte('\n')

	nameW, cwdW := m.columnWidths(w)
	line(formatRow("", "NAME", "STATE", "STATUS", "RAM", "SWAP", "PROCS", "CWD", nameW, cwdW), colHeadStyle)
	if len(m.groups) == 0 {
		line("  no agent groups found", helpStyle)
	}
	for i, g := range m.groups {
		b.WriteString(m.renderRow(i, g, nameW, cwdW, w))
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	if m.err != nil {
		line("error: "+m.err.Error()+" (retrying)", errStyle)
	}
	if m.confirm != nil {
		line(m.confirm.prompt(), promptStyle)
	}
	if m.busy {
		line(spinFrames[m.frame%len(spinFrames)]+" working…", lipgloss.NewStyle())
	} else if m.msg != "" {
		line(m.msg, lipgloss.NewStyle())
	}
	line(helpText, helpStyle)
	return strings.TrimSuffix(b.String(), "\n")
}

func (m model) header() string {
	return fmt.Sprintf("agent-freeze   RAM free %s   swap free %s   frozen %d (freed %s)",
		human.Bytes(m.status.MemAvailable), human.Bytes(m.status.SwapFree),
		m.status.Frozen, human.Bytes(m.status.Freed))
}

func (p *pendingFreeze) prompt() string {
	if p.kind == confirmBusy {
		return fmt.Sprintf("%d of the selected agents are busy (mid-task). Freeze anyway? y/n", p.nBusy)
	}
	return fmt.Sprintf("Not enough free swap (need %s, have %s). Freeze anyway? y/n",
		human.Bytes(p.need), human.Bytes(p.have))
}

// columnWidths sizes the name column to the longest label (capped) and gives
// the rest of the width to cwd.
func (m model) columnWidths(w int) (nameW, cwdW int) {
	nameW = len("NAME")
	for _, g := range m.groups {
		nameW = max(nameW, runeLen(g.Name+marker(g)))
	}
	nameW = min(nameW, maxNameW)
	avail := w - fixedW
	if nameW > avail-minCwdW {
		nameW = max(minNameW, avail-minCwdW)
	}
	return nameW, max(0, avail-nameW)
}

func marker(g freezer.GroupView) string {
	switch {
	case g.Self:
		return " (you)"
	case g.Daemon:
		return " [daemon]"
	}
	return ""
}

func (m model) renderRow(i int, g freezer.GroupView, nameW, cwdW, w int) string {
	cur := " "
	if i == m.cursor {
		cur = ">"
	}
	box := "[ ]"
	if m.selected[g.Key] {
		box = "[x]"
	}
	// Truncate the name, not the marker, so (you)/[daemon] stay visible.
	mk := marker(g)
	name := truncRight(g.Name, max(1, nameW-runeLen(mk))) + mk
	status := g.Status
	if status == "" {
		status = "-"
	}
	plain := formatRow(cur+" "+box, name, g.State, status,
		human.Bytes(g.RSS), human.Bytes(g.Swap), strconv.Itoa(len(g.PIDs)),
		truncLeft(human.Path(g.Cwd), cwdW), nameW, cwdW)
	plain = truncRight(plain, w)

	st := lipgloss.NewStyle()
	switch g.State {
	case "frozen":
		st = st.Faint(true)
	case "partial":
		st = st.Foreground(partialFg)
	}
	if g.Self {
		st = st.Faint(true)
	}
	if i == m.cursor {
		st = st.Bold(true)
	}
	if g.Status != "busy" {
		return st.Render(plain)
	}
	// Highlight the STATUS cell of busy rows.
	r := []rune(plain)
	off := prefixW + nameW + 1 + stateW + 1
	if len(r) <= off {
		return st.Render(plain)
	}
	end := min(off+statusW, len(r))
	return st.Render(string(r[:off])) +
		st.Foreground(busyFg).Bold(true).Render(string(r[off:end])) +
		st.Render(string(r[end:]))
}

func formatRow(prefix, name, state, status, ram, swap, procs, cwd string, nameW, cwdW int) string {
	s := fmt.Sprintf("%-*s%s %s %s %*s %*s %*s ",
		prefixW, prefix, pad(name, nameW), pad(state, stateW), pad(status, statusW),
		ramW, ram, swapW, swap, procsW, procs)
	if cwdW > 0 {
		s += truncRight(cwd, cwdW)
	}
	return strings.TrimRight(s, " ")
}

// pad left-aligns s in exactly n runes, truncating if needed.
func pad(s string, n int) string {
	s = truncRight(s, n)
	return s + strings.Repeat(" ", n-runeLen(s))
}

func runeLen(s string) int { return len([]rune(s)) }

// truncRight keeps the start of s, ending in "…" if it was cut.
func truncRight(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	return string(r[:n-1]) + "…"
}

// truncLeft keeps the end of s (the most specific part of a path).
func truncLeft(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	return "…" + string(r[len(r)-n+1:])
}
