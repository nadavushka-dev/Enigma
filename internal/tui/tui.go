// Package tui is Enigma's terminal UI (SPEC §12.2): Machines and Files side
// by side, Activity below. It is a client of the local daemon through a
// Backend.
//
// Each pane lives in its own file and implements pane. Panes never block:
// anything slow (the daemon, ssh) runs in a tea.Cmd, and dialogs are
// opened by returning one of the open* messages from a command.
package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

// statusEvery is how often the daemon's status is refreshed.
const statusEvery = 2 * time.Second

// Shared is the state every pane may read.
type Shared struct {
	Backend   Backend
	Status    daemon.StatusReport
	Machines  []proto.MachineRecord
	StatusErr error // the daemon is unreachable
}

// KeyHelp is one key binding, for the footer and the help overlay.
type KeyHelp struct{ Key, Does string }

// pane is one of the three panes.
type pane interface {
	Title() string
	// Update gets key messages while focused, and every other message
	// (statusMsg, EventsMsg, window size, ...) always.
	Update(msg tea.Msg, s *Shared) tea.Cmd
	// View renders the pane's body, exactly width x height cells.
	View(s *Shared, width, height int, focused bool) string
	Help() []KeyHelp
}

// ---- messages ----------------------------------------------------------------

// StatusMsg is a fresh daemon status (sent to every pane).
type StatusMsg struct {
	Status   daemon.StatusReport
	Machines []proto.MachineRecord
	Err      error
}

// EventsMsg carries new activity events (sent to every pane).
type EventsMsg struct {
	Events []engine.Event
	Err    error
}

// RefreshMsg asks for a status refresh now (after a change).
type RefreshMsg struct{}

// OpenConfirm asks a yes/no question; Yes runs on y. An optional Alt key
// runs AltCmd (e.g. "u" = remove and uninstall).
type OpenConfirm struct {
	Question         string
	Yes              tea.Cmd
	AltKey, AltLabel string
	Alt              tea.Cmd
}

// OpenProgress runs a long operation and shows its output live.
type OpenProgress struct {
	Title string
	Run   func(ctx context.Context, out io.Writer) error
}

// OpenViewer shows two texts side by side, lines marked as different.
type OpenViewer struct {
	Title       string
	Left, Right Column
}

type Column struct {
	Title string
	Lines []string
	Marks []bool // Marks[i]: line i differs
}

// ShowError puts a one-line error in the footer.
type ShowError struct{ Err error }

type tickMsg struct{}

// ---- model -----------------------------------------------------------------------

type Model struct {
	s       *Shared
	panes   []pane
	focus   int
	width   int
	height  int
	help    bool
	modal   modal
	lastErr string
	lastSeq uint64
}

// New builds the TUI over b.
func New(b Backend) *Model {
	s := &Shared{Backend: b}
	return &Model{s: s, panes: []pane{newMachines(), newFiles(), newActivity()}}
}

// Run starts the TUI in the terminal.
func Run(b Backend) error {
	_, err := tea.NewProgram(New(b), tea.WithAltScreen()).Run()
	return err
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.fetchStatus(), m.pollEvents())
}

func (m *Model) fetchStatus() tea.Cmd {
	b := m.s.Backend
	return func() tea.Msg {
		st, err := b.Status()
		if err != nil {
			return StatusMsg{Err: err}
		}
		ms, err := b.Machines()
		return StatusMsg{Status: st, Machines: ms, Err: err}
	}
}

func (m *Model) pollEvents() tea.Cmd {
	b, after := m.s.Backend, m.lastSeq
	return func() tea.Msg {
		evs, err := b.Events(after, 15*time.Second)
		return EventsMsg{Events: evs, Err: err}
	}
}

func (m *Model) broadcast(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for _, p := range m.panes {
		cmds = append(cmds, p.Update(msg, m.s))
	}
	return tea.Batch(cmds...)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, m.broadcast(msg)
	case StatusMsg:
		m.s.StatusErr = msg.Err
		if msg.Err == nil {
			m.s.Status, m.s.Machines = msg.Status, msg.Machines
		}
		return m, tea.Batch(m.broadcast(msg), tea.Tick(statusEvery, func(time.Time) tea.Msg { return tickMsg{} }))
	case tickMsg:
		return m, m.fetchStatus()
	case RefreshMsg:
		return m, m.fetchStatus()
	case EventsMsg:
		for _, ev := range msg.Events {
			m.lastSeq = max(m.lastSeq, ev.Seq)
		}
		next := m.pollEvents()
		if msg.Err != nil { // daemon down: try again in a while
			next = tea.Tick(statusEvery, func(time.Time) tea.Msg { return pollMsg{} })
		}
		return m, tea.Batch(m.broadcast(msg), next)
	case pollMsg:
		return m, m.pollEvents()
	case ShowError:
		m.lastErr = msg.Err.Error()
		return m, nil
	case OpenConfirm:
		m.modal = &confirmModal{q: msg}
		return m, nil
	case OpenProgress:
		pm, cmd := startProgress(msg)
		m.modal = pm
		return m, cmd
	case OpenViewer:
		m.modal = &viewerModal{v: msg}
		return m, nil
	case progressLineMsg, progressDoneMsg:
		if pm, ok := m.modal.(*progressModal); ok {
			return m, pm.update(msg)
		}
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, m.broadcast(msg)
}

type pollMsg struct{}

func (m *Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.modal != nil {
		cmd, closed := m.modal.key(k)
		if closed {
			m.modal = nil
		}
		return m, cmd
	}
	if m.help {
		m.help = false // any key closes it
		return m, nil
	}
	m.lastErr = ""
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "?":
		m.help = true
		return m, nil
	case "tab":
		m.focus = (m.focus + 1) % len(m.panes)
		return m, nil
	case "shift+tab":
		m.focus = (m.focus + len(m.panes) - 1) % len(m.panes)
		return m, nil
	}
	return m, m.panes[m.focus].Update(k, m.s)
}

// layout: Machines | Files on top, Activity below, footer last.
func (m *Model) View() string {
	if m.width == 0 {
		return "starting…"
	}
	var banner string
	if m.s.StatusErr != nil {
		banner = StyleBad.Render(fit(" "+m.s.StatusErr.Error(), m.width))
	}
	footer := m.footer()
	bodyH := m.height - 1
	if banner != "" {
		bodyH--
	}
	actH := max(5, bodyH/4)
	topH := bodyH - actH
	leftW := max(26, m.width*35/100)
	rightW := m.width - leftW
	render := func(i, w, h int) string {
		p := m.panes[i]
		return box(p.Title(), p.View(m.s, w-2, h-2, i == m.focus), w, h, i == m.focus)
	}
	top := lipgloss.JoinHorizontal(lipgloss.Top, render(0, leftW, topH), render(1, rightW, topH))
	screen := lipgloss.JoinVertical(lipgloss.Left, top, render(2, m.width, actH))
	if banner != "" {
		screen = banner + "\n" + screen
	}
	screen += "\n" + footer
	switch {
	case m.modal != nil:
		return overlay(screen, m.modal.view(m.width, m.height), m.width, m.height)
	case m.help:
		return overlay(screen, m.helpView(), m.width, m.height)
	}
	return screen
}

func (m *Model) footer() string {
	if m.lastErr != "" {
		return StyleBad.Render(fit(" "+m.lastErr, m.width))
	}
	var parts []string
	for _, h := range m.panes[m.focus].Help() {
		parts = append(parts, StyleAccent.Render(h.Key)+" "+h.Does)
	}
	parts = append(parts, StyleAccent.Render("tab")+" focus", StyleAccent.Render("?")+" help", StyleAccent.Render("q")+" quit")
	return fit(" "+strings.Join(parts, "  "), m.width)
}

func (m *Model) helpView() string {
	var b strings.Builder
	b.WriteString(StyleAccent.Render("Enigma") + " — keys\n\n")
	row := func(k, d string) { fmt.Fprintf(&b, "  %-12s %s\n", k, d) }
	row("tab/S-tab", "move focus between panes")
	row("j/k ↑/↓", "move")
	row("g/G", "first / last")
	row("ctrl+d/u", "half page down / up")
	for _, p := range m.panes {
		b.WriteString("\n" + StyleAccent.Render(p.Title()) + "\n")
		for _, h := range p.Help() {
			row(h.Key, h.Does)
		}
	}
	b.WriteString("\n")
	row("?", "this help (any key closes it)")
	row("q", "quit")
	return b.String()
}

// overlay centers dialog over screen.
func overlay(screen, dialog string, width, height int) string {
	d := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colAccent).Padding(0, 1).Render(dialog)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, d,
		lipgloss.WithWhitespaceChars(" "))
}
