package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	historyMax  = 500
	historyHelp = "↑/↓ j/k scroll  h/esc back  q quit"
	// header, blank, title, blank, blank before the footer, footer line, help line
	historyChrome = 7
)

func (m model) historyKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "h", "esc":
		m.history = false
	case "up", "k":
		m.histOffset = max(m.histOffset-1, 0)
	case "down", "j":
		m.histOffset = min(m.histOffset+1, max(len(m.events)-m.historyRows(), 0))
	}
	return m, nil
}

func (m model) historyRows() int {
	if m.height <= 0 {
		return 20
	}
	return max(m.height-historyChrome, 3)
}

// historyView renders the visible history rows, newest first, each at most w runes wide.
func (m model) historyView(w int) []string {
	lines := []string{titleStyle.Render(truncRight("  History (newest first)", w)), ""}
	if len(m.events) == 0 {
		return append(lines, helpStyle.Render(truncRight("  nothing yet: freezes and thaws show up here", w)))
	}
	start := min(m.histOffset, max(len(m.events)-m.historyRows(), 0))
	end := min(start+m.historyRows(), len(m.events))
	for _, e := range m.events[start:end] {
		what, st := e.Action, lipgloss.NewStyle()
		if e.Failed() {
			what, st = "could not "+e.Verb(), errStyle
		}
		row := "  " + pad(e.Time.Local().Format("Jan 2 15:04"), 13) + pad(e.Who(), 6) + pad(what, 17) +
			pad(e.Name, 32) + " " + e.Details()
		lines = append(lines, st.Render(truncRight(row, w)))
	}
	return lines
}
