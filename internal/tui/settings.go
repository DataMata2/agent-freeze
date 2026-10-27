package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/DataMata2/agent-freeze/internal/auto"
)

const (
	settingsHelp = "↑/↓ j/k move  ←/→ change  enter type a value  s/esc back  q quit"
	editHelp     = "type a value  enter save  esc cancel"
	labelW       = 18
	valueW       = 14
)

// The settings screen has the mode on row 0, then auto.Settings.
func (m model) settingsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if m.editing {
		return m.editKey(k)
	}
	switch s {
	case "s", "esc":
		m.settings = false
	case "up", "k":
		m.setCursor = clamp(m.setCursor-1, 0, len(auto.Settings))
	case "down", "j":
		m.setCursor = clamp(m.setCursor+1, 0, len(auto.Settings))
	case "left", "h", "right", "l", "enter":
		if m.setCursor == 0 {
			return m.startModeSwitch()
		}
		if s == "enter" {
			m.editing, m.input, m.msg = true, "", ""
			return m, nil
		}
		dir := 1
		if s == "left" || s == "h" {
			dir = -1
		}
		setting := auto.Settings[m.setCursor-1]
		return m, saveSettingCmd(setting, func(c auto.Config) (auto.Config, error) { return setting.Step(c, dir), nil })
	}
	return m, nil
}

func (m model) editKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		m.editing = false
	case tea.KeyEnter:
		m.editing = false
		setting, value := auto.Settings[m.setCursor-1], m.input
		return m, saveSettingCmd(setting, func(c auto.Config) (auto.Config, error) { return setting.Set(c, value) })
	case tea.KeyBackspace:
		if r := []rune(m.input); len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
	case tea.KeyRunes, tea.KeySpace:
		m.input += string(k.Runes)
	}
	return m, nil
}

func saveSettingCmd(s auto.Setting, change func(auto.Config) (auto.Config, error)) tea.Cmd {
	return func() tea.Msg {
		cfg, err := auto.LoadConfig(auto.DefaultPath())
		if err == nil {
			cfg, err = change(cfg)
		}
		if err == nil {
			err = auto.SaveConfig(auto.DefaultPath(), cfg)
		}
		if err != nil {
			return settingsMsg{err: err}
		}
		return settingsMsg{text: s.Label + ": " + s.Display(cfg)}
	}
}

// settingsView renders the settings rows, each at most w runes wide.
func (m model) settingsView(w int) []string {
	lines := []string{titleStyle.Render(truncRight("  Auto mode settings", w)), ""}
	row := func(i int, label, value, help string) {
		cur, st := " ", lipgloss.NewStyle()
		if i == m.setCursor {
			cur, st = ">", st.Bold(true)
			if m.editing {
				value = m.input + "_"
			}
		}
		left := truncRight(cur+" "+pad(label, labelW)+pad(value, valueW), w)
		lines = append(lines, st.Render(left)+helpStyle.Render(truncRight(help, w-runeLen(left))))
	}
	row(0, "mode", m.auto.Mode, "m in the session list does the same")
	for i, s := range auto.Settings {
		row(i+1, s.Label, s.Display(m.auto), s.Help)
	}
	return lines
}

func (m model) settingsHelpText() string {
	if m.editing {
		return editHelp
	}
	return settingsHelp
}

func (m model) startModeSwitch() (tea.Model, tea.Cmd) {
	m.busy, m.frame, m.msg = true, 0, ""
	m.opGen++
	return m, tea.Batch(m.modeCmd(), spinCmd(m.opGen))
}
