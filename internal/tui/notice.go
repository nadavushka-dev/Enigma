package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/enigma/internal/problems"
)

// The notice line (SPEC §13.5): one line above the key hints. A failed
// action (ShowError) stays in red until the next key; an action's result
// (ShowNotice) shows for noticeFor; otherwise the standing problems, or
// "all good".

// noticeFor is how long an action's result stays on the notice line.
const noticeFor = 4 * time.Second

// ShowNotice puts an action's result on the notice line for a while.
type ShowNotice struct{ Text string }

// noticeExpiredMsg clears the notice with sequence seq (a newer one stays).
type noticeExpiredMsg struct{ seq uint64 }

// JumpMsg asks the panes to put their cursor on a path (Files) or a
// member (Machines); the model focuses the pane that took it.
type JumpMsg struct {
	Path string // relative to ~
	Peer string // machine name
}

func (m *Model) showNotice(text string) tea.Cmd {
	m.noticeSeq++
	m.notice = text
	seq := m.noticeSeq
	return tea.Tick(noticeFor, func(time.Time) tea.Msg { return noticeExpiredMsg{seq} })
}

func (m *Model) noticeLine() string {
	switch {
	case m.lastErr != "":
		return StyleBad.Render(fit(" "+m.lastErr, m.width))
	case m.notice != "":
		return StyleOK.Render(fit(" "+m.notice, m.width))
	}
	ps := m.s.Status.Problems
	if len(ps) == 0 {
		return StyleDim.Render(fit(" all good", m.width))
	}
	style, mark := StyleWarn, "~"
	if ps[0].Severity == problems.Error {
		style, mark = StyleBad, "!"
	}
	hint := StyleAccent.Render("p")
	text := " " + mark + " " + problems.Summary(ps)
	return style.Render(fit(text, m.width-lipgloss.Width(hint)-1)) + " " + hint
}

// ---- problems modal -----------------------------------------------------------

type problemsModal struct {
	ps  []problems.Problem
	cur int
}

func (p *problemsModal) key(k tea.KeyMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "esc", "q", "p":
		return nil, true
	case "j", "down":
		p.cur = min(p.cur+1, max(0, len(p.ps)-1))
	case "k", "up":
		p.cur = max(0, p.cur-1)
	case "g":
		p.cur = 0
	case "G":
		p.cur = max(0, len(p.ps)-1)
	case "enter":
		if p.cur >= len(p.ps) {
			return nil, true
		}
		pr := p.ps[p.cur]
		if pr.Path == "" && pr.Peer == "" {
			return nil, false
		}
		return func() tea.Msg { return JumpMsg{Path: pr.Path, Peer: pr.Peer} }, true
	}
	return nil, false
}

func (p *problemsModal) view(width, height int) string {
	w := max(30, width*3/4)
	var b strings.Builder
	b.WriteString(StyleAccent.Render("Problems") + "\n\n")
	if len(p.ps) == 0 {
		b.WriteString(StyleOK.Render("no problems") + "\n\n" + StyleDim.Render("esc closes"))
		return b.String()
	}
	rows := max(2, (height-8)/2)
	first := max(0, min(p.cur-rows/2, len(p.ps)-rows))
	for i := first; i < len(p.ps) && i < first+rows; i++ {
		pr := p.ps[i]
		style, mark := StyleWarn, "~"
		if pr.Severity == problems.Error {
			style, mark = StyleBad, "!"
		}
		line := fmt.Sprintf("%s %s  %s", mark, pr.Message, StyleDim.Render(ago(pr.Since)))
		hint := "   " + pr.Hint
		if i == p.cur {
			b.WriteString(StyleSelected.Render(fit(line, w)) + "\n")
		} else {
			b.WriteString(style.Render(fit(line, w)) + "\n")
		}
		if pr.Hint != "" {
			b.WriteString(StyleDim.Render(fit(hint, w)) + "\n")
		}
	}
	b.WriteString("\n" + StyleDim.Render("j/k move  enter go to it  esc closes"))
	return b.String()
}

// ago is a problem's age for a human: "just now", "3m", "2h", "1d".
func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
