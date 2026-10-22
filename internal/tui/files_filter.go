package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/enigma/internal/fuzzy"
)

// The Files pane's / filter (SPEC §12.4): the rows become a flat list of
// matching paths, best first, in one of two scopes.
type filterScope int

const (
	scopeTree filterScope = iota // the folders opened so far
	scopeHome                    // the home index
)

type fileFilter struct {
	query   string
	scope   filterScope
	hits    []fuzzy.Hit
	sel     int
	scroll  int
	saved   int // the tree cursor to restore on esc
	index   homeIndex
	prog    fuzzy.Progress
	ticking bool // one tick chain at a time while the index builds
}

// homeIndex is what the filter needs from fuzzy.Index; tests fake it.
type homeIndex interface {
	Snapshot() ([]fuzzy.Entry, fuzzy.Progress)
}

// indexTickMsg asks the filter to look at the index again while it builds.
type indexTickMsg struct{}

const indexEvery = 100 * time.Millisecond

func (p *filesPane) openFilter(s *Shared) tea.Cmd {
	p.filter = &fileFilter{saved: p.cursor}
	p.refilter(s)
	return nil
}

func (p *filesPane) closeFilter() { p.filter = nil }

// candidates is what the current scope offers; folders end in "/".
func (p *filesPane) candidates(s *Shared) []string {
	f := p.filter
	if f.scope == scopeTree {
		var out []string
		var walk func(n *fileNode)
		walk = func(n *fileNode) {
			for _, c := range n.children {
				rel := c.rel
				if c.dir {
					rel += "/"
				}
				out = append(out, rel)
				if c.loaded {
					walk(c)
				}
			}
		}
		walk(p.root)
		return out
	}
	if f.index == nil {
		f.index = s.Backend.HomeIndex()
	}
	ents, pr := f.index.Snapshot()
	f.prog = pr
	return fuzzy.Paths(ents)
}

func (p *filesPane) refilter(s *Shared) tea.Cmd {
	f := p.filter
	f.hits = fuzzy.Rank(f.query, p.candidates(s))
	f.sel = min(f.sel, max(0, len(f.hits)-1))
	if f.scope == scopeHome && !f.prog.Done && !f.ticking {
		f.ticking = true
		return tea.Tick(indexEvery, func(time.Time) tea.Msg { return indexTickMsg{} })
	}
	return nil
}

func (p *filesPane) filterTick(s *Shared) tea.Cmd {
	if p.filter == nil {
		return nil
	}
	p.filter.ticking = false
	if p.filter.scope != scopeHome || p.filter.prog.Done {
		return nil
	}
	return p.refilter(s)
}

func (p *filesPane) filterKey(k tea.KeyMsg, s *Shared) tea.Cmd {
	f := p.filter
	switch k.String() {
	case "esc":
		p.closeFilter()
		p.cursor = min(f.saved, max(0, len(p.rows)-1))
		return nil
	case "enter":
		p.closeFilter()
		if f.sel < len(f.hits) {
			p.jumpTo(s, strings.TrimSuffix(f.hits[f.sel].Path, "/"))
		}
		return nil
	case "tab":
		f.scope = 1 - f.scope
		f.sel = 0
		return p.refilter(s)
	case "down", "ctrl+n":
		f.sel = min(f.sel+1, max(0, len(f.hits)-1))
		return nil
	case "up", "ctrl+p":
		f.sel = max(0, f.sel-1)
		return nil
	case "backspace":
		if r := []rune(f.query); len(r) > 0 {
			f.query = string(r[:len(r)-1])
		}
		f.sel = 0
		return p.refilter(s)
	}
	if k.Type == tea.KeySpace {
		f.query += " "
		f.sel = 0
		return p.refilter(s)
	}
	if k.Type == tea.KeyRunes && !k.Alt {
		f.query += string(k.Runes)
		f.sel = 0
		return p.refilter(s)
	}
	return nil
}

// jumpTo puts the cursor on rel, opening its parents.
func (p *filesPane) jumpTo(s *Shared, rel string) {
	n := p.root
	for _, name := range strings.Split(rel, "/") {
		if !n.dir || n.symlink {
			return
		}
		p.expand(s, n)
		var next *fileNode
		for _, c := range n.children {
			if c.name == name {
				next = c
				break
			}
		}
		if next == nil {
			break
		}
		n = next
	}
	p.rebuild()
	if n != p.root {
		p.selectNode(n)
	}
}

// indexStatus is the filter line's note on the home index.
func indexStatus(pr fuzzy.Progress) string {
	switch {
	case !pr.Done:
		return fmt.Sprintf("indexing… %d", pr.Count)
	case pr.Truncated:
		return fmt.Sprintf("first %d paths", pr.Count)
	}
	return ""
}

type segment struct {
	text    string
	matched bool
}

// highlightSegments splits path into runs of matched and plain runes.
func highlightSegments(path string, positions []int) []segment {
	hit := map[int]bool{}
	for _, i := range positions {
		hit[i] = true
	}
	var out []segment
	for i, r := range []rune(path) {
		if n := len(out); n > 0 && out[n-1].matched == hit[i] {
			out[n-1].text += string(r)
			continue
		}
		out = append(out, segment{string(r), hit[i]})
	}
	return out
}

func (p *filesPane) filterView(s *Shared, width, height int, focused bool) string {
	f := p.filter
	left := "/ " + f.query
	if focused {
		left += "▏"
	}
	var right string
	switch f.scope {
	case scopeTree:
		right = StyleAccent.Render("tree") + " " + StyleDim.Render("~")
	case scopeHome:
		right = StyleDim.Render("tree") + " " + StyleAccent.Render("~")
		if st := indexStatus(f.prog); st != "" {
			right = StyleDim.Render(st) + "  " + right
		}
	}
	lines := []string{fit(padBetween(StyleAccent.Render(left), right, width), width)}
	listH := height - 1
	if listH <= 0 {
		return strings.Join(lines, "\n")
	}
	if f.sel < f.scroll {
		f.scroll = f.sel
	}
	if f.sel >= f.scroll+listH {
		f.scroll = f.sel - listH + 1
	}
	f.scroll = max(0, min(f.scroll, max(0, len(f.hits)-listH)))
	for i := f.scroll; i < f.scroll+listH; i++ {
		if i >= len(f.hits) {
			lines = append(lines, fit("", width))
			continue
		}
		h := f.hits[i]
		selected := focused && i == f.sel
		mark := p.markRel(s, strings.TrimSuffix(h.Path, "/"), strings.HasSuffix(h.Path, "/"))
		var b strings.Builder
		b.WriteString(" ")
		for _, seg := range highlightSegments(h.Path, h.Positions) {
			switch {
			case seg.matched && selected:
				b.WriteString(styleMatchSel.Render(seg.text))
			case seg.matched:
				b.WriteString(styleMatch.Render(seg.text))
			case selected:
				b.WriteString(StyleSelected.Render(seg.text))
			default:
				b.WriteString(seg.text)
			}
		}
		line := b.String()
		switch {
		case selected:
			line = StyleSelected.Render(" ") + strings.TrimPrefix(line, " ")
			if mark != "" {
				line = padBetween(line, StyleSelected.Render(mark), width)
			}
			if pad := width - lipgloss.Width(line); pad > 0 {
				line += StyleSelected.Render(strings.Repeat(" ", pad))
			}
		case mark != "":
			line = padBetween(line, markStyle(mark).Render(mark), width)
		}
		lines = append(lines, fit(line, width))
	}
	return strings.Join(lines, "\n")
}
