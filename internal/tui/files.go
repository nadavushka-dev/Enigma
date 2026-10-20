package tui

import tea "github.com/charmbracelet/bubbletea"

// filesPane is the Files pane (SPEC §12.2). Stub: to be built.
type filesPane struct{}

func newFiles() *filesPane { return &filesPane{} }

func (p *filesPane) Title() string { return "Files" }

func (p *filesPane) Update(msg tea.Msg, s *Shared) tea.Cmd { return nil }

func (p *filesPane) View(s *Shared, width, height int, focused bool) string {
	return StyleDim.Render("(not built yet)")
}

func (p *filesPane) Help() []KeyHelp { return nil }
