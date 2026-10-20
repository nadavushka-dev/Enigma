package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// modal is a dialog over the panes; it takes every key while open.
type modal interface {
	key(tea.KeyMsg) (cmd tea.Cmd, closed bool)
	view(width, height int) string
}

// ---- confirm ---------------------------------------------------------------------

type confirmModal struct{ q OpenConfirm }

func (c *confirmModal) key(k tea.KeyMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "y", "Y":
		return c.q.Yes, true
	case "n", "N", "esc", "q":
		return nil, true
	}
	if c.q.AltKey != "" && k.String() == c.q.AltKey {
		return c.q.Alt, true
	}
	return nil, false
}

func (c *confirmModal) view(width, _ int) string {
	keys := StyleAccent.Render("y") + " yes  " + StyleAccent.Render("n") + " no"
	if c.q.AltKey != "" {
		keys += "  " + StyleAccent.Render(c.q.AltKey) + " " + c.q.AltLabel
	}
	return lipgloss.NewStyle().MaxWidth(width-6).Render(c.q.Question) + "\n\n" + keys
}

// ---- progress --------------------------------------------------------------------

type progressLineMsg struct{ line string }
type progressDoneMsg struct{ err error }

type progressModal struct {
	title  string
	lines  []string
	done   bool
	err    error
	cancel context.CancelFunc
	next   tea.Cmd
}

// lineWriter turns writes into whole lines on a channel.
type lineWriter struct {
	mu  sync.Mutex
	buf string
	ch  chan<- string
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf += string(p)
	for {
		i := strings.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.ch <- w.buf[:i]
		w.buf = w.buf[i+1:]
	}
}

func startProgress(o OpenProgress) (*progressModal, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	lines := make(chan string, 256)
	done := make(chan error, 1)
	go func() {
		w := &lineWriter{ch: lines}
		err := o.Run(ctx, w)
		if w.buf != "" {
			lines <- w.buf
		}
		close(lines)
		done <- err
	}()
	var wait tea.Cmd
	wait = func() tea.Msg {
		if l, ok := <-lines; ok {
			return progressLineMsg{l}
		}
		return progressDoneMsg{<-done}
	}
	return &progressModal{title: o.Title, cancel: cancel, next: wait}, wait
}

func (p *progressModal) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case progressLineMsg:
		p.lines = append(p.lines, msg.line)
		return p.next
	case progressDoneMsg:
		p.done, p.err = true, msg.err
		p.cancel()
		return func() tea.Msg { return RefreshMsg{} }
	}
	return nil
}

func (p *progressModal) key(k tea.KeyMsg) (tea.Cmd, bool) {
	if p.done && (k.String() == "esc" || k.String() == "enter" || k.String() == "q") {
		return nil, true
	}
	if !p.done && k.String() == "esc" {
		p.cancel() // the operation sees its context cancelled
	}
	return nil, false
}

func (p *progressModal) view(width, height int) string {
	w := max(20, width*3/4)
	show := p.lines
	if room := max(3, height-10); len(show) > room {
		show = show[len(show)-room:]
	}
	var b strings.Builder
	b.WriteString(StyleAccent.Render(p.title) + "\n\n")
	for _, l := range show {
		b.WriteString(fit(l, w) + "\n")
	}
	switch {
	case !p.done:
		b.WriteString("\n" + StyleDim.Render("working… (esc cancels)"))
	case p.err != nil:
		b.WriteString("\n" + StyleBad.Render(fit("failed: "+p.err.Error(), w)) + "\n" + StyleDim.Render("esc closes"))
	default:
		b.WriteString("\n" + StyleOK.Render("done") + "  " + StyleDim.Render("esc closes"))
	}
	return b.String()
}

// ---- side-by-side viewer -----------------------------------------------------------

type viewerModal struct {
	v      OpenViewer
	scroll int
}

func (v *viewerModal) key(k tea.KeyMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "esc", "q", "enter":
		return nil, true
	case "j", "down":
		v.scroll++
	case "k", "up":
		v.scroll = max(0, v.scroll-1)
	case "g":
		v.scroll = 0
	}
	return nil, false
}

func (v *viewerModal) view(width, height int) string {
	colW := max(10, (width-10)/2)
	rows := max(3, height-8)
	n := max(len(v.v.Left.Lines), len(v.v.Right.Lines))
	v.scroll = min(v.scroll, max(0, n-rows))
	col := func(c Column) []string {
		out := []string{StyleAccent.Render(fit(c.Title, colW))}
		for i := v.scroll; i < v.scroll+rows && i < n; i++ {
			l := ""
			if i < len(c.Lines) {
				l = c.Lines[i]
			}
			mark := "  "
			if i < len(c.Marks) && c.Marks[i] {
				mark = StyleWarn.Render("▌ ")
			}
			out = append(out, mark+fit(fmt.Sprintf("%3d %s", i+1, l), colW-2))
		}
		return out
	}
	left, right := col(v.v.Left), col(v.v.Right)
	var b strings.Builder
	b.WriteString(StyleAccent.Render(v.v.Title) + "\n\n")
	for i := 0; i < len(left) || i < len(right); i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		b.WriteString(fit(l, colW) + "  " + r + "\n")
	}
	b.WriteString("\n" + StyleDim.Render("j/k scroll  esc closes"))
	return b.String()
}
