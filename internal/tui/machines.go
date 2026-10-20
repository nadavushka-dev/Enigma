package tui

import tea "github.com/charmbracelet/bubbletea"

// machinesPane is the Machines pane (SPEC §12.2). Stub: to be built.
type machinesPane struct{}

func newMachines() *machinesPane { return &machinesPane{} }

func (p *machinesPane) Title() string { return "Machines" }

func (p *machinesPane) Update(msg tea.Msg, s *Shared) tea.Cmd { return nil }

func (p *machinesPane) View(s *Shared, width, height int, focused bool) string {
	return StyleDim.Render("(not built yet)")
}

func (p *machinesPane) Help() []KeyHelp { return nil }
