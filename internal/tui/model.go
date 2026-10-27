// Package tui is the interactive Bubble Tea front end over freezer.API.
package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DataMata2/agent-freeze/internal/auto"
	"github.com/DataMata2/agent-freeze/internal/freezer"
	"github.com/DataMata2/agent-freeze/internal/human"
)

const (
	tickInterval = 2 * time.Second        // list refresh
	spinInterval = 120 * time.Millisecond // spinner frame while busy
)

// tick builds timer commands. Tests replace it with a version that returns
// the message at once, so draining commands never sleeps.
var tick = tea.Tick

const selfRefusal = "that's this agent's own session — use the CLI with --force-self"

// Run runs the TUI in the alternate screen until the user quits.
func Run(api freezer.API, setAuto func(on bool) error) error {
	m := newModel(api)
	m.setAuto = setAuto
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

type confirmKind int

const (
	confirmBusy confirmKind = iota // some targets are mid-task
	confirmSwap                    // targets' RSS exceeds free swap
)

// pendingFreeze is a freeze waiting for the user to answer a y/n prompt.
type pendingFreeze struct {
	targets []freezer.GroupView
	kind    confirmKind
	nBusy   int    // confirmBusy: targets with Status "busy"
	need    uint64 // confirmSwap: Σ RSS of targets
	have    uint64 // confirmSwap: free swap
}

type refreshMsg struct {
	groups   []freezer.GroupView
	status   freezer.Status
	err      error
	auto     auto.Config
	autoErr  error
	lastAuto string
}

type settingsMsg struct {
	text string
	err  error
}

type tickMsg struct{}

// spinMsg advances the spinner. gen ties it to one operation, so a tick left
// over from a finished operation cannot start a second chain.
type spinMsg struct{ gen int }

type opDoneMsg struct {
	thaw    bool
	results []freezer.Result
}

type model struct {
	api      freezer.API
	groups   []freezer.GroupView
	status   freezer.Status
	cursor   int
	selected map[string]bool // by Group.Key
	busy     bool            // an operation is running
	frame    int             // spinner frame
	opGen    int             // bumped per operation; see spinMsg
	msg      string
	confirm  *pendingFreeze
	width    int
	height   int
	err      error
	auto     auto.Config
	lastAuto string
	setAuto  func(on bool) error

	settings  bool // the settings screen is open
	setCursor int
	editing   bool // typing a value on the settings screen
	input     string
}

func newModel(api freezer.API) model {
	return model{api: api, selected: map[string]bool{}, auto: auto.Defaults()}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.refreshCmd(), tickCmd())
}

func (m model) refreshCmd() tea.Cmd {
	api := m.api
	return func() tea.Msg {
		groups, err := api.List()
		if err != nil {
			return refreshMsg{err: err}
		}
		st, err := api.Status()
		if err != nil {
			return refreshMsg{err: err}
		}
		msg := refreshMsg{groups: groups, status: st}
		msg.auto, msg.autoErr = auto.LoadConfig(auto.DefaultPath())
		if e, ok := auto.LastEvent(auto.LogPath()); ok {
			msg.lastAuto = e.Time.Local().Format("Jan 2 15:04") + " " + e.String()
		}
		return msg
	}
}

func (m model) modeCmd() tea.Cmd {
	on, setAuto := m.auto.Mode != "auto", m.setAuto
	return func() tea.Msg {
		if err := setAuto(on); err != nil {
			return settingsMsg{err: err}
		}
		if on {
			return settingsMsg{text: "auto mode on: unused sessions are frozen when memory or CPU runs short"}
		}
		return settingsMsg{text: "auto mode off"}
	}
}

func pinCmd(g freezer.GroupView) tea.Cmd {
	return func() tea.Msg {
		cfg, err := auto.LoadConfig(auto.DefaultPath())
		if err == nil {
			cfg = cfg.TogglePin(g)
			err = auto.SaveConfig(auto.DefaultPath(), cfg)
		}
		if err != nil {
			return settingsMsg{err: err}
		}
		if cfg.Pinned(g) {
			return settingsMsg{text: "pinned " + g.Name + ": auto mode never freezes it"}
		}
		return settingsMsg{text: "unpinned " + g.Name}
	}
}

func tickCmd() tea.Cmd {
	return tick(tickInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func spinCmd(gen int) tea.Cmd {
	return tick(spinInterval, func(time.Time) tea.Msg { return spinMsg{gen: gen} })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case refreshMsg:
		if msg.err != nil {
			m.err = msg.err // keep the last good list; the next tick retries
			return m, nil
		}
		m.err = msg.autoErr
		m.groups, m.status = msg.groups, msg.status
		m.auto, m.lastAuto = msg.auto, msg.lastAuto
		m.cursor = clamp(m.cursor, 0, len(m.groups)-1)
		present := make(map[string]bool, len(m.groups))
		for _, g := range m.groups {
			present[g.Key] = true
		}
		for k := range m.selected {
			if !present[k] {
				delete(m.selected, k)
			}
		}
		return m, nil
	case tickMsg:
		if m.busy {
			return m, tickCmd()
		}
		return m, tea.Batch(m.refreshCmd(), tickCmd())
	case spinMsg:
		if !m.busy || msg.gen != m.opGen {
			return m, nil
		}
		m.frame++
		return m, spinCmd(m.opGen)
	case settingsMsg:
		m.busy = false
		m.msg = msg.text
		if msg.err != nil {
			m.msg = "error: " + msg.err.Error()
		}
		return m, m.refreshCmd()
	case opDoneMsg:
		m.busy = false
		m.selected = map[string]bool{}
		m.msg = summary(msg.thaw, msg.results)
		return m, m.refreshCmd()
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" || (s == "q" && !m.editing) {
		return m, tea.Quit
	}
	if m.busy {
		return m, nil
	}
	if m.settings {
		return m.settingsKey(k)
	}
	if m.confirm != nil {
		p := m.confirm
		m.confirm = nil
		if s != "y" {
			m.msg = "cancelled"
			return m, nil
		}
		if p.kind == confirmBusy {
			return m.swapGuard(p.targets)
		}
		return m.startFreeze(p.targets)
	}
	switch s {
	case "up", "k":
		m.cursor = clamp(m.cursor-1, 0, len(m.groups)-1)
	case "down", "j":
		m.cursor = clamp(m.cursor+1, 0, len(m.groups)-1)
	case " ", "space":
		if g, ok := m.current(); ok {
			m.selected[g.Key] = !m.selected[g.Key]
		}
	case "a":
		m.toggleAll()
	case "f":
		targets := m.chosen()
		for _, g := range targets {
			if g.Self {
				m.msg = selfRefusal
				return m, nil
			}
		}
		return m.busyGuard(targets)
	case "F":
		var targets []freezer.GroupView
		for _, g := range m.groups {
			if eligible(g) && g.State != "frozen" {
				targets = append(targets, g)
			}
		}
		return m.busyGuard(targets)
	case "u":
		return m.startThaw(m.chosen())
	case "U":
		var targets []freezer.GroupView
		for _, g := range m.groups {
			if g.State == "frozen" || g.State == "partial" {
				targets = append(targets, g)
			}
		}
		return m.startThaw(targets)
	case "m":
		return m.startModeSwitch()
	case "s":
		m.settings, m.msg = true, ""
	case "p":
		if g, ok := m.current(); ok {
			return m, pinCmd(g)
		}
	case "r":
		return m, m.refreshCmd()
	}
	return m, nil
}

func eligible(g freezer.GroupView) bool { return !g.Self && !g.Daemon }

func (m model) current() (freezer.GroupView, bool) {
	if m.cursor < 0 || m.cursor >= len(m.groups) {
		return freezer.GroupView{}, false
	}
	return m.groups[m.cursor], true
}

// chosen returns the selected rows in list order, or the current row if none.
func (m model) chosen() []freezer.GroupView {
	var out []freezer.GroupView
	for _, g := range m.groups {
		if m.selected[g.Key] {
			out = append(out, g)
		}
	}
	if len(out) == 0 {
		if g, ok := m.current(); ok {
			out = append(out, g)
		}
	}
	return out
}

// toggleAll selects every eligible row, or clears the selection if all
// eligible rows are already selected.
func (m *model) toggleAll() {
	all := true
	for _, g := range m.groups {
		if eligible(g) && !m.selected[g.Key] {
			all = false
			break
		}
	}
	m.selected = map[string]bool{}
	if all {
		return
	}
	for _, g := range m.groups {
		if eligible(g) {
			m.selected[g.Key] = true
		}
	}
}

func (m model) busyGuard(targets []freezer.GroupView) (tea.Model, tea.Cmd) {
	if len(targets) == 0 {
		m.msg = "nothing to freeze"
		return m, nil
	}
	n := 0
	for _, g := range targets {
		if g.Status == "busy" {
			n++
		}
	}
	if n > 0 {
		m.confirm = &pendingFreeze{targets: targets, kind: confirmBusy, nBusy: n}
		return m, nil
	}
	return m.swapGuard(targets)
}

func (m model) swapGuard(targets []freezer.GroupView) (tea.Model, tea.Cmd) {
	var need uint64
	for _, g := range targets {
		need += g.RSS
	}
	if need > m.status.SwapFree {
		m.confirm = &pendingFreeze{targets: targets, kind: confirmSwap, need: need, have: m.status.SwapFree}
		return m, nil
	}
	return m.startFreeze(targets)
}

func (m model) startFreeze(targets []freezer.GroupView) (tea.Model, tea.Cmd) {
	api := m.api
	m.busy, m.frame, m.msg = true, 0, ""
	m.opGen++
	op := func() tea.Msg {
		res := make([]freezer.Result, 0, len(targets))
		for _, g := range targets {
			res = append(res, api.Freeze(g, false))
		}
		return opDoneMsg{results: res}
	}
	return m, tea.Batch(op, spinCmd(m.opGen))
}

func (m model) startThaw(targets []freezer.GroupView) (tea.Model, tea.Cmd) {
	if len(targets) == 0 {
		m.msg = "nothing to thaw"
		return m, nil
	}
	api := m.api
	m.busy, m.frame, m.msg = true, 0, ""
	m.opGen++
	op := func() tea.Msg {
		res := make([]freezer.Result, 0, len(targets))
		for _, g := range targets {
			res = append(res, api.Thaw(g))
		}
		return opDoneMsg{thaw: true, results: res}
	}
	return m, tea.Batch(op, spinCmd(m.opGen))
}

// summary renders an operation's results, e.g. "froze 3, freed 1.2G",
// "thawed 2", or "froze 1, freed 340M; failed: x: err (+1 more)".
func summary(thaw bool, results []freezer.Result) string {
	ok := 0
	var freed uint64
	var failed []freezer.Result
	for _, r := range results {
		if !r.OK {
			failed = append(failed, r)
			continue
		}
		ok++
		if r.RSSBefore > r.RSSAfter {
			freed += r.RSSBefore - r.RSSAfter
		}
	}
	var s string
	if ok > 0 {
		if thaw {
			s = fmt.Sprintf("thawed %d", ok)
		} else {
			s = fmt.Sprintf("froze %d, freed %s", ok, human.Bytes(freed))
		}
	}
	if len(failed) > 0 {
		f := fmt.Sprintf("failed: %s: %s", failed[0].Name, failed[0].Error)
		if len(failed) > 1 {
			f += fmt.Sprintf(" (+%d more)", len(failed)-1)
		}
		if s == "" {
			return f
		}
		s += "; " + f
	}
	return s
}

func clamp(v, lo, hi int) int {
	if v > hi {
		v = hi
	}
	if v < lo {
		v = lo
	}
	return v
}
