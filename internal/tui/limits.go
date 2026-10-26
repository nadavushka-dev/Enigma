package tui

import (
	"fmt"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/enigma/internal/engine"
)

// editLimits is the L key (SPEC §13.2): two prompts, then the fleet's
// limits are set for every machine (SPEC §8).
func (m *Model) editLimits() tea.Cmd {
	cur := m.s.Status.Limits
	b := m.s.Backend
	return func() tea.Msg {
		return OpenPrompt{Title: "Limits: size per file", Label: "size per file", Initial: engine.FormatSize(cur.MaxFileSize),
			Submit: func(size string) tea.Cmd {
				n, err := engine.ParseSize(size)
				if err != nil {
					return showErr(fmt.Errorf("file size %v", err))
				}
				return func() tea.Msg {
					return OpenPrompt{Title: "Limits: files per folder", Label: "files per folder", Initial: strconv.Itoa(cur.MaxFiles),
						Submit: func(files string) tea.Cmd {
							k, err := strconv.Atoi(files)
							if err != nil || k <= 0 {
								return showErr(fmt.Errorf("files per folder wants a positive number, not %q", files))
							}
							return func() tea.Msg {
								l, err := b.SetLimits(engine.Limits{MaxFileSize: n, MaxFiles: k})
								if err != nil {
									return ShowError{Err: err}
								}
								return ShowNotice{Text: "limits set for every machine: " + l.String()}
							}
						}}
				}
			}}
	}
}
