package tui

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/merge"
)

// maxKept is how many events the pane keeps (the daemon's ring is the same size).
const maxKept = 500

// activityPane is the Activity pane (SPEC §12.2): the daemon's recent
// events, newest at the bottom, following new ones until the user moves up.
type activityPane struct {
	events []engine.Event
	maxSeq uint64
	cursor int
	scroll int
	follow bool
	height int // of the last View, for paging
}

func newActivity() *activityPane { return &activityPane{follow: true} }

func (p *activityPane) Title() string { return "Activity" }

func (p *activityPane) Help() []KeyHelp {
	return []KeyHelp{{"enter", "view conflict"}}
}

func (p *activityPane) Update(msg tea.Msg, s *Shared) tea.Cmd {
	switch msg := msg.(type) {
	case EventsMsg:
		for _, ev := range msg.Events {
			if ev.Seq <= p.maxSeq {
				continue
			}
			p.maxSeq = ev.Seq
			p.events = append(p.events, ev)
		}
		if drop := len(p.events) - maxKept; drop > 0 {
			p.events = append(p.events[:0:0], p.events[drop:]...)
			p.cursor = max(0, p.cursor-drop)
			p.scroll = max(0, p.scroll-drop)
		}
	case tea.KeyMsg:
		return p.key(msg, s)
	}
	return nil
}

func (p *activityPane) key(k tea.KeyMsg, s *Shared) tea.Cmd {
	n := len(p.events)
	if n == 0 {
		return nil
	}
	last := n - 1
	if p.follow {
		p.cursor = last
	}
	half := max(1, p.height/2)
	switch k.String() {
	case "j", "down":
		p.cursor++
	case "k", "up":
		p.cursor--
	case "ctrl+d":
		p.cursor += half
	case "ctrl+u":
		p.cursor -= half
	case "g":
		p.cursor = 0
	case "G":
		p.cursor = last
	case "enter":
		return p.open(p.events[p.cursor], s)
	default:
		return nil
	}
	p.cursor = min(max(p.cursor, 0), last)
	p.follow = p.cursor == last
	return nil
}

// open shows a conflict's original and copy side by side.
func (p *activityPane) open(ev engine.Event, s *Shared) tea.Cmd {
	if ev.Kind != engine.EvConflict {
		return nil
	}
	b, orig, copyPath := s.Backend, ev.Detail, ev.Path
	return func() tea.Msg {
		o, err := b.ReadFile(orig)
		if err != nil {
			return ShowError{Err: readErr(err, "the original is gone")}
		}
		c, err := b.ReadFile(copyPath)
		if err != nil {
			return ShowError{Err: readErr(err, "the conflict copy is gone")}
		}
		om, cm := merge.LineChanges(o, c)
		return OpenViewer{
			Title: "~/" + orig,
			Left:  Column{Title: "original ~/" + orig, Lines: lines(o), Marks: om},
			Right: Column{Title: "conflict copy ~/" + copyPath, Lines: lines(c), Marks: cm},
		}
	}
}

func readErr(err error, gone string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New(gone)
	}
	return err
}

// lines splits text the way merge.LineChanges counts lines.
func lines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func (p *activityPane) View(s *Shared, width, height int, focused bool) string {
	if height <= 0 {
		return ""
	}
	p.height = height
	out := make([]string, height)
	n := len(p.events)
	if n == 0 {
		out[0] = fit(StyleDim.Render("no activity yet"), width)
		for i := 1; i < height; i++ {
			out[i] = fit("", width)
		}
		return strings.Join(out, "\n")
	}
	if p.follow {
		p.cursor = n - 1
	}
	p.cursor = min(max(p.cursor, 0), n-1)
	// Keep the cursor visible.
	p.scroll = min(p.scroll, n-height)
	p.scroll = min(max(p.scroll, 0), p.cursor)
	if p.cursor >= p.scroll+height {
		p.scroll = p.cursor - height + 1
	}
	for i := 0; i < height; i++ {
		idx := p.scroll + i
		if idx >= n {
			out[i] = fit("", width)
			continue
		}
		l := fit(formatEvent(p.events[idx]), width)
		if focused && idx == p.cursor {
			l = StyleSelected.Render(l)
		}
		out[i] = l
	}
	return strings.Join(out, "\n")
}

// formatEvent is one event's line: time, icon, text; styled by kind.
func formatEvent(ev engine.Event) string {
	home := func(p string) string { return "~/" + p }
	var text string
	style := func(s ...string) string { return strings.Join(s, "") }
	switch ev.Kind {
	case engine.EvReceived:
		text = "← " + home(ev.Path) + " from " + ev.Peer
	case engine.EvMerged:
		text = "⇄ " + home(ev.Path) + " merged with " + ev.Peer
	case engine.EvChanged:
		text = "→ " + home(ev.Path)
	case engine.EvDeleted:
		text = "✗ " + home(ev.Path)
		if ev.Peer != "" {
			text += " by " + ev.Peer
		}
	case engine.EvConflict:
		text = "! conflict: " + home(ev.Detail) + " (copy " + home(ev.Path) + ")"
		style = StyleWarn.Render
	case engine.EvEntryAdded:
		text = "+ " + home(ev.Path) + " synced"
	case engine.EvEntryRemoved:
		text = "- " + home(ev.Path) + " no longer synced"
	case engine.EvLinkUp:
		text = "● " + ev.Peer + " linked"
		style = StyleOK.Render
	case engine.EvLinkDown:
		text = "○ " + ev.Peer + " down"
		style = StyleDim.Render
	case engine.EvUpgrade:
		text = "↑ " + ev.Peer + ": " + ev.Detail
	case engine.EvError:
		text = ev.Path + ": " + ev.Detail
		style = StyleBad.Render
	default:
		text = fmt.Sprintf("%s %s %s", ev.Kind, ev.Path, ev.Detail)
	}
	return style(ev.Time.Local().Format("15:04:05") + " " + text)
}
