package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	colAccent = lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#9D99FF"}
	colDim    = lipgloss.AdaptiveColor{Light: "#9B9B9B", Dark: "#6C6C6C"}
	colOK     = lipgloss.AdaptiveColor{Light: "#1F8A4C", Dark: "#4FD18B"}
	colWarn   = lipgloss.AdaptiveColor{Light: "#B26A00", Dark: "#F2B04A"}
	colBad    = lipgloss.AdaptiveColor{Light: "#C0392B", Dark: "#FF6B6B"}

	StyleDim      = lipgloss.NewStyle().Foreground(colDim)
	StyleOK       = lipgloss.NewStyle().Foreground(colOK)
	StyleWarn     = lipgloss.NewStyle().Foreground(colWarn)
	StyleBad      = lipgloss.NewStyle().Foreground(colBad)
	StyleAccent   = lipgloss.NewStyle().Foreground(colAccent)
	StyleSelected = lipgloss.NewStyle().Reverse(true)
	styleMatch    = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styleMatchSel = lipgloss.NewStyle().Reverse(true).Foreground(colAccent).Bold(true)
	styleBorder   = lipgloss.NewStyle().Foreground(colDim)
	styleFocused  = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
)

// fit cuts or pads s to exactly width cells.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = lipgloss.NewStyle().MaxWidth(width).Render(s)
	if pad := width - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// box draws a titled frame of exactly width x height around body lines.
func box(title, body string, width, height int, focused bool) string {
	if width < 4 || height < 2 {
		return ""
	}
	border := styleBorder
	if focused {
		border = styleFocused
	}
	inner := width - 2
	t := " " + title + " "
	if lipgloss.Width(t) > inner {
		t = fit(t, inner)
	}
	top := border.Render("┌"+t+strings.Repeat("─", max(0, inner-lipgloss.Width(t)))) + border.Render("┐")
	lines := strings.Split(body, "\n")
	out := []string{top}
	for i := 0; i < height-2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out = append(out, border.Render("│")+fit(l, inner)+border.Render("│"))
	}
	out = append(out, border.Render("└"+strings.Repeat("─", inner)+"┘"))
	return strings.Join(out, "\n")
}
