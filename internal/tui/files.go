package tui

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/fsx"
)

// filesPane is the Files pane (SPEC §12.2): a lazy tree rooted at ~.
// Folders are read only when expanded; the root is expanded at start.
type filesPane struct {
	root   *fileNode
	rows   []*fileNode // visible nodes, in order (the root excluded)
	cursor int
	scroll int
	height int         // last rendered height, for paging
	filter *fileFilter // the / filter line, when open
}

// fileNode is one item of the tree.
type fileNode struct {
	name     string
	rel      string // slash-separated, relative to ~; "" for the root
	depth    int
	dir      bool
	symlink  bool
	other    bool // not a regular file, dir or symlink
	size     int64
	parent   *fileNode
	expanded bool
	loaded   bool
	children []*fileNode
}

var ignoreMatcher = fsx.NewMatcher(nil)

func newFiles() *filesPane { return &filesPane{} }

func (p *filesPane) Title() string { return "Files ~/" }

func (p *filesPane) Help() []KeyHelp {
	if p.filter != nil {
		return []KeyHelp{{"type", "filter"}, {"tab", "scope"}, {"↑/↓", "move"}, {"enter", "jump"}, {"esc", "cancel"}}
	}
	return []KeyHelp{{"l/h", "open/close"}, {"a", "add"}, {"x", "remove"}, {"/", "filter"}}
}

func (p *filesPane) MoreHelp() (string, []KeyHelp) {
	return "Files filter (/)", []KeyHelp{
		{"tab", "scope: tree (folders opened so far) / ~ (the home index)"},
		{"↑/↓ ^p/^n", "move"},
		{"enter", "jump to it in the tree"},
		{"esc", "cancel"},
	}
}

func (p *filesPane) grabsKeys() bool { return p.filter != nil }

// ensure builds the root on the first use.
func (p *filesPane) ensure(s *Shared) {
	if p.root != nil || s == nil || s.Backend == nil {
		return
	}
	p.root = &fileNode{dir: true, depth: -1}
	p.expand(s, p.root)
	p.rebuild()
}

// load reads a folder's children once (folders first, then files, by name).
func (p *filesPane) load(s *Shared, n *fileNode) {
	if n.loaded {
		return
	}
	n.loaded = true
	ents, err := os.ReadDir(filepath.Join(s.Backend.Home(), filepath.FromSlash(n.rel)))
	if err != nil {
		return
	}
	for _, e := range ents {
		c := &fileNode{name: e.Name(), rel: path.Join(n.rel, e.Name()), depth: n.depth + 1, parent: n}
		mode := e.Type()
		switch {
		case mode&os.ModeSymlink != 0:
			c.symlink = true
		case mode.IsDir():
			c.dir = true
		case mode.IsRegular():
			if info, err := e.Info(); err == nil {
				c.size = info.Size()
			}
		default:
			c.other = true
		}
		n.children = append(n.children, c)
	}
	sort.SliceStable(n.children, func(i, j int) bool {
		a, b := n.children[i], n.children[j]
		if a.dir != b.dir {
			return a.dir
		}
		return a.name < b.name
	})
}

// reload re-reads every folder that was read, keeping the nodes (and so
// what is open) of names that are still there.
func (p *filesPane) reload(s *Shared, n *fileNode) {
	if n == nil || !n.loaded {
		return
	}
	old := map[string]*fileNode{}
	for _, c := range n.children {
		old[c.name] = c
	}
	n.loaded, n.children = false, nil
	p.load(s, n)
	for i, c := range n.children {
		if o := old[c.name]; o != nil && o.dir == c.dir && o.symlink == c.symlink {
			o.size, o.other = c.size, c.other
			n.children[i] = o
			p.reload(s, o)
		}
	}
}

func (p *filesPane) expand(s *Shared, n *fileNode) {
	if !n.dir {
		return
	}
	p.load(s, n)
	n.expanded = true
}

// rebuild recomputes the visible rows, keeping the selection on the same
// path when possible.
func (p *filesPane) rebuild() {
	var sel string
	if p.cursor < len(p.rows) {
		sel = p.rows[p.cursor].rel
	}
	p.rows = p.rows[:0]
	var walk func(n *fileNode)
	walk = func(n *fileNode) {
		for _, c := range n.children {
			p.rows = append(p.rows, c)
			if c.expanded {
				walk(c)
			}
		}
	}
	walk(p.root)
	p.cursor = 0
	for i, r := range p.rows {
		if r.rel == sel {
			p.cursor = i
			break
		}
	}
}

func (p *filesPane) current() *fileNode {
	if p.cursor < len(p.rows) {
		return p.rows[p.cursor]
	}
	return nil
}

func (p *filesPane) selectNode(n *fileNode) {
	for i, r := range p.rows {
		if r == n {
			p.cursor = i
			return
		}
	}
}

func (p *filesPane) Update(msg tea.Msg, s *Shared) tea.Cmd {
	p.ensure(s)
	switch msg.(type) {
	case StatusMsg, RefreshMsg:
		p.reload(s, p.root) // the disk may have changed
		p.rebuild()
		return nil
	}
	switch msg := msg.(type) {
	case JumpMsg:
		if msg.Path != "" {
			p.closeFilter()
			p.jumpTo(s, msg.Path)
		}
		return nil
	case indexTickMsg:
		return p.filterTick(s)
	case rankedMsg:
		p.ranked(msg)
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	if p.filter != nil {
		return p.filterKey(k, s)
	}
	n := p.current()
	half := max(1, p.height/2)
	switch k.String() {
	case "j", "down":
		p.cursor = min(p.cursor+1, max(0, len(p.rows)-1))
	case "k", "up":
		p.cursor = max(0, p.cursor-1)
	case "g":
		p.cursor = 0
	case "G":
		p.cursor = max(0, len(p.rows)-1)
	case "ctrl+d":
		p.cursor = min(p.cursor+half, max(0, len(p.rows)-1))
	case "ctrl+u":
		p.cursor = max(0, p.cursor-half)
	case "l", "enter", "right":
		if n == nil || !n.dir {
			return nil
		}
		if n.expanded {
			if len(n.children) > 0 {
				p.cursor++
			}
			return nil
		}
		p.expand(s, n)
		p.rebuild()
	case "h", "left":
		if n == nil {
			return nil
		}
		if n.dir && n.expanded {
			n.expanded = false
			p.rebuild()
		} else if n.parent != nil && n.parent != p.root {
			p.selectNode(n.parent)
		}
	case "a":
		return p.add(s, n)
	case "x":
		return p.remove(s, n)
	case "/":
		return p.openFilter(s)
	}
	return nil
}

func (p *filesPane) add(s *Shared, n *fileNode) tea.Cmd {
	if n == nil {
		return nil
	}
	if e := entryFor(s, n.rel); e != nil {
		return showErr(fmt.Errorf("~/%s is already synced", n.rel))
	}
	abs := filepath.Join(s.Backend.Home(), filepath.FromSlash(n.rel))
	b := s.Backend
	return func() tea.Msg {
		if err := b.Add(abs); err != nil {
			return ShowError{Err: err}
		}
		return RefreshMsg{}
	}
}

func (p *filesPane) remove(s *Shared, n *fileNode) tea.Cmd {
	if n == nil {
		return nil
	}
	if e := entryFor(s, n.rel); e == nil || e.Path != n.rel {
		return showErr(fmt.Errorf("~/%s is not a synced entry", n.rel))
	}
	abs := filepath.Join(s.Backend.Home(), filepath.FromSlash(n.rel))
	b := s.Backend
	q := OpenConfirm{Question: fmt.Sprintf("Stop syncing ~/%s on every machine? The files stay.", n.rel),
		Yes: func() tea.Msg {
			if err := b.Remove(abs); err != nil {
				return ShowError{Err: err}
			}
			return RefreshMsg{}
		}}
	return func() tea.Msg { return q }
}

func showErr(err error) tea.Cmd { return func() tea.Msg { return ShowError{Err: err} } }

// entryFor is the entry rel is, or is inside of; nil otherwise.
func entryFor(s *Shared, rel string) *engine.EntryStatus {
	for i := range s.Status.Entries {
		e := &s.Status.Entries[i]
		if e.Path == rel || (e.Dir && strings.HasPrefix(rel, e.Path+"/")) {
			return e
		}
	}
	return nil
}

// mark is the row's right-aligned status text ("" for none).
func (p *filesPane) mark(s *Shared, n *fileNode) string { return p.markRel(s, n.rel, n.dir) }

// markRel is mark for a path relative to ~ (a folder when dir).
func (p *filesPane) markRel(s *Shared, rel string, dir bool) string {
	e := entryFor(s, rel)
	if e == nil {
		if dir {
			for _, en := range s.Status.Entries {
				if strings.HasPrefix(en.Path, rel+"/") {
					return "contains synced"
				}
			}
		}
		return ""
	}
	if e.Path == rel {
		if e.Missing {
			return "missing here"
		}
		return "synced"
	}
	for _, sk := range e.Skipped {
		if sk == rel {
			return "skipped"
		}
	}
	for _, ol := range e.OverLimit {
		if ol == rel {
			return "over limit"
		}
	}
	if ignoreMatcher.Ignored(strings.TrimPrefix(rel, e.Path+"/")) {
		return "" // inside the folder, but not synced
	}
	return "·"
}

func markStyle(mark string) lipgloss.Style {
	switch mark {
	case "synced":
		return StyleOK
	case "missing here", "skipped", "over limit":
		return StyleWarn
	}
	return StyleDim
}

// dimmed: ignored names, oversized files, symlinks and other non-regular items.
func (p *filesPane) dimmed(s *Shared, n *fileNode) bool {
	if n.symlink || n.other {
		return true
	}
	maxSize := s.Status.Limits.MaxFileSize
	if maxSize == 0 { // a daemon from before fleet limits
		maxSize = engine.DefaultMaxFileSize
	}
	if !n.dir && n.size > maxSize {
		return true
	}
	rel := n.rel
	if e := entryFor(s, rel); e != nil && e.Path != rel {
		rel = strings.TrimPrefix(rel, e.Path+"/")
	}
	return ignoreMatcher.Ignored(rel)
}

func (p *filesPane) View(s *Shared, width, height int, focused bool) string {
	p.ensure(s)
	p.height = height
	if height <= 0 {
		return ""
	}
	if p.filter != nil {
		return p.filterView(s, width, height, focused)
	}
	// keep the cursor visible
	if p.cursor < p.scroll {
		p.scroll = p.cursor
	}
	if p.cursor >= p.scroll+height {
		p.scroll = p.cursor - height + 1
	}
	p.scroll = max(0, min(p.scroll, max(0, len(p.rows)-height)))
	lines := make([]string, 0, height)
	for i := p.scroll; i < p.scroll+height; i++ {
		if i >= len(p.rows) {
			lines = append(lines, fit("", width))
			continue
		}
		n := p.rows[i]
		name := n.name
		switch {
		case n.symlink:
			name += " -> "
		case n.dir:
			name += "/"
		}
		left := " " + strings.Repeat("  ", n.depth) + name
		mark := p.mark(s, n)
		dim := p.dimmed(s, n) && mark != "·"
		var line string
		switch {
		case focused && i == p.cursor:
			text := left
			if mark != "" {
				text = padBetween(left, mark, width)
			}
			line = StyleSelected.Render(fit(text, width))
		default:
			l := left
			if dim {
				l = StyleDim.Render(left)
			}
			if mark != "" {
				l = padBetween(l, markStyle(mark).Render(mark), width)
			}
			line = fit(l, width)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// padBetween puts right at the end of a width-wide line, after left.
func padBetween(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}
