package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// OpenPrompt asks for one line of text (SPEC §13.2). Submit runs on enter
// with what was typed; esc cancels. Every key goes to the prompt.
type OpenPrompt struct {
	Title, Label string
	Initial      string
	Submit       func(string) tea.Cmd
}

type promptModal struct {
	p  OpenPrompt
	in textinput.Model
}

func newPrompt(p OpenPrompt) *promptModal {
	in := textinput.New()
	in.Prompt = p.Label + ": "
	in.SetValue(p.Initial)
	in.CursorEnd()
	in.Focus()
	return &promptModal{p: p, in: in}
}

func (m *promptModal) key(k tea.KeyMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "esc":
		return nil, true
	case "enter":
		return m.p.Submit(strings.TrimSpace(m.in.Value())), true
	}
	var cmd tea.Cmd
	m.in, cmd = m.in.Update(k)
	return cmd, false
}

func (m *promptModal) view(width, _ int) string {
	m.in.Width = max(10, min(60, width-10))
	return StyleAccent.Render(m.p.Title) + "\n\n" + m.in.View() + "\n\n" + StyleDim.Render("enter ok  esc cancel")
}

// OpenPick lets the user check items in a list (SPEC §13.2): space
// toggles, enter submits the checked labels in list order, esc cancels.
type OpenPick struct {
	Title  string
	Items  []PickItem
	Submit func(checked []string) tea.Cmd
}

type PickItem struct {
	Label   string
	Checked bool
}

type pickModal struct {
	p   OpenPick
	cur int
}

func (m *pickModal) key(k tea.KeyMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "esc", "q":
		return nil, true
	case "j", "down":
		m.cur = min(m.cur+1, max(0, len(m.p.Items)-1))
	case "k", "up":
		m.cur = max(0, m.cur-1)
	case " ", "x":
		if m.cur < len(m.p.Items) {
			m.p.Items[m.cur].Checked = !m.p.Items[m.cur].Checked
		}
	case "enter":
		var out []string
		for _, it := range m.p.Items {
			if it.Checked {
				out = append(out, it.Label)
			}
		}
		return m.p.Submit(out), true
	}
	return nil, false
}

func (m *pickModal) view(width, height int) string {
	w := max(20, min(60, width-10))
	var b strings.Builder
	b.WriteString(StyleAccent.Render(m.p.Title) + "\n\n")
	rows := max(3, height-8)
	first := max(0, min(m.cur-rows/2, len(m.p.Items)-rows))
	for i := first; i < len(m.p.Items) && i < first+rows; i++ {
		it := m.p.Items[i]
		box := "[ ] "
		if it.Checked {
			box = "[x] "
		}
		line := fit(box+it.Label, w)
		if i == m.cur {
			line = StyleSelected.Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + StyleDim.Render("space toggle  enter ok  esc cancel"))
	return b.String()
}
