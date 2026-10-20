package tui

import tea "github.com/charmbracelet/bubbletea"

// activityPane is the Activity pane (SPEC §12.2). Stub: to be built.
type activityPane struct{}

func newActivity() *activityPane { return &activityPane{} }

func (p *activityPane) Title() string { return "Activity" }

func (p *activityPane) Update(msg tea.Msg, s *Shared) tea.Cmd { return nil }

func (p *activityPane) View(s *Shared, width, height int, focused bool) string {
	return StyleDim.Render("(not built yet)")
}

func (p *activityPane) Help() []KeyHelp { return nil }
