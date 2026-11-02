package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

// adoptMode is which screen the first-add dialog is showing. A list is
// always a selection; choosing is always a small menu, never a hidden
// in-place toggle (SPEC §13.2, D22).
type adoptMode int

const (
	adoptMenu  adoptMode = iota // what to do (whole entry, or one file)
	adoptFiles                  // the per-file list of a folder
	adoptPick                   // keep both / mine wins for one file
)

type adoptItem struct {
	label, hint string
}

// adoptRow is one differing file in the per-file list.
type adoptRow struct {
	path     string // relative to the entry root
	machines []string
	keepBoth bool
}

// adoptModal is the first-add dialog (SPEC §13.2, D22): the machines that
// have the path, then a choice. A folder can apply one choice to every file
// or be decided file by file.
type adoptModal struct {
	q      OpenAdopt
	submit func(proto.FirstAddPolicy, map[string]proto.FirstAddPolicy) tea.Cmd

	mode    adoptMode
	menu    []adoptItem
	menuCur int
	rows    []adoptRow
	rowCur  int
	pickRow int
	pickCur int
	off     int
	fit     int
}

func newAdoptModal(q OpenAdopt, submit func(proto.FirstAddPolicy, map[string]proto.FirstAddPolicy) tea.Cmd) *adoptModal {
	m := &adoptModal{q: q, submit: submit, mode: adoptMenu}
	for _, f := range q.Report.Files {
		if f.State == engine.AdoptionDifferent {
			m.rows = append(m.rows, adoptRow{path: f.Path, machines: f.Machines, keepBoth: true})
		}
	}
	if q.Report.Dir {
		m.menu = []adoptItem{
			{"Keep both for every file (recommended)", "each differing file keeps a visible conflict copy"},
			{"This machine wins for every file", "their copies are backed up and replaced"},
			{"Choose per file…", "decide one file at a time"},
			{"Cancel", "nothing is added"},
		}
	} else {
		m.menu = []adoptItem{
			{"Keep both (recommended)", "each other machine's copy stays as a visible conflict copy"},
			{"This machine wins", "each other machine's copy is backed up and replaced"},
			{"Cancel", "nothing is added"},
		}
	}
	return m
}

func (m *adoptModal) key(k tea.KeyMsg) (tea.Cmd, bool) {
	switch m.mode {
	case adoptMenu:
		return m.menuKey(k)
	case adoptFiles:
		return m.filesKey(k)
	case adoptPick:
		return m.pickKey(k)
	}
	return nil, false
}

func (m *adoptModal) menuKey(k tea.KeyMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "j", "down":
		m.menuCur = min(m.menuCur+1, len(m.menu)-1)
	case "k", "up":
		m.menuCur = max(0, m.menuCur-1)
	case "g":
		m.menuCur = 0
	case "G":
		m.menuCur = len(m.menu) - 1
	case "enter":
		return m.choose()
	case "esc", "q":
		return notice("cancelled; ~/" + m.q.Rel + " was not added"), true
	}
	return nil, false
}

func (m *adoptModal) choose() (tea.Cmd, bool) {
	if m.q.Report.Dir {
		switch m.menuCur {
		case 0:
			return m.submit(proto.FirstAddKeepBoth, nil), true
		case 1:
			return m.submit(proto.FirstAddReplace, nil), true
		case 2:
			m.mode, m.off, m.rowCur = adoptFiles, 0, 0
			return nil, false
		default:
			return notice("cancelled; ~/" + m.q.Rel + " was not added"), true
		}
	}
	switch m.menuCur {
	case 0:
		return m.submit(proto.FirstAddKeepBoth, nil), true
	case 1:
		return m.submit(proto.FirstAddReplace, nil), true
	default:
		return notice("cancelled; ~/" + m.q.Rel + " was not added"), true
	}
}

func (m *adoptModal) filesKey(k tea.KeyMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "j", "down":
		m.rowCur = min(m.rowCur+1, max(0, len(m.rows)-1))
	case "k", "up":
		m.rowCur = max(0, m.rowCur-1)
	case "g":
		m.rowCur = 0
	case "G":
		m.rowCur = max(0, len(m.rows)-1)
	case "enter":
		if m.rowCur < len(m.rows) {
			m.pickRow = m.rowCur
			m.pickCur = 0
			if !m.rows[m.pickRow].keepBoth {
				m.pickCur = 1
			}
			m.mode = adoptPick
		}
	case "K":
		for i := range m.rows {
			m.rows[i].keepBoth = true
		}
	case "M":
		for i := range m.rows {
			m.rows[i].keepBoth = false
		}
	case "A":
		return m.submit(m.filesChoice()), true
	case "esc", "q":
		m.mode, m.off = adoptMenu, 0
	}
	return nil, false
}

func (m *adoptModal) pickKey(k tea.KeyMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "j", "down":
		m.pickCur = min(m.pickCur+1, 2)
	case "k", "up":
		m.pickCur = max(0, m.pickCur-1)
	case "enter":
		if m.pickCur < 2 {
			m.rows[m.pickRow].keepBoth = m.pickCur == 0
		}
		m.mode = adoptFiles
	case "esc", "q":
		m.mode = adoptFiles
	}
	return nil, false
}

// filesChoice keeps the folder default at keep-both (so a file on a machine
// that was offline is never silently replaced) and names the files the user
// chose to replace.
func (m *adoptModal) filesChoice() (proto.FirstAddPolicy, map[string]proto.FirstAddPolicy) {
	var byPath map[string]proto.FirstAddPolicy
	for _, r := range m.rows {
		if !r.keepBoth && r.path != "" {
			if byPath == nil {
				byPath = map[string]proto.FirstAddPolicy{}
			}
			byPath[r.path] = proto.FirstAddReplace
		}
	}
	return proto.FirstAddKeepBoth, byPath
}

func (m *adoptModal) view(width, height int) string {
	w := max(20, width-6)
	head := m.head()
	m.fit = max(1, height-2-strings.Count(head, "\n")-8)

	var body string
	switch m.mode {
	case adoptMenu:
		body = m.menuView(w)
	case adoptFiles:
		body = m.filesView(w)
	case adoptPick:
		body = m.pickView(w)
	}
	return head + body
}

func (m *adoptModal) menuView(w int) string {
	var b strings.Builder
	prompt := "What should happen?"
	if m.q.Report.Dir {
		prompt = fmt.Sprintf("%d differing %s. What should happen?", len(m.rows), engine.Plural(len(m.rows), "file", "files"))
	}
	b.WriteString("\n" + prompt + "\n")
	for i, it := range m.menu {
		cursor, label := "  ", it.label
		if i == m.menuCur {
			cursor, label = StyleAccent.Render("> "), StyleAccent.Render(it.label)
		}
		b.WriteString(cursor + fit(label, w-2) + "\n")
		if it.hint != "" {
			b.WriteString("    " + StyleDim.Render(fit(it.hint, w-6)) + "\n")
		}
	}
	b.WriteString("\n" + StyleAccent.Render("j/k") + " move  " + StyleAccent.Render("enter") + " choose  " + StyleAccent.Render("esc") + " cancel")
	return b.String()
}

func (m *adoptModal) filesView(w int) string {
	if len(m.rows) == 0 {
		return "\nnothing differs\n\n" + StyleAccent.Render("esc") + " back"
	}
	if m.rowCur < m.off {
		m.off = m.rowCur
	}
	if m.rowCur >= m.off+m.fit {
		m.off = m.rowCur - m.fit + 1
	}
	end := min(len(m.rows), m.off+m.fit)
	var b strings.Builder
	b.WriteString("\nChoose for each file\n")
	b.WriteString("  " + fit("FILE", w-18) + "CHOICE\n")
	for i := m.off; i < end; i++ {
		r := m.rows[i]
		cursor := "  "
		if i == m.rowCur {
			cursor = StyleAccent.Render("> ")
		}
		label := "~/" + m.q.Rel
		if r.path != "" {
			label += "/" + r.path
		}
		if len(r.machines) > 0 {
			label += " (" + strings.Join(r.machines, ", ") + ")"
		}
		choice := StyleOK.Render("keep both")
		if !r.keepBoth {
			choice = StyleWarn.Render("mine wins")
		}
		b.WriteString(cursor + fit(label, w-20) + fit(choice, 12) + "\n")
	}
	if end < len(m.rows) {
		b.WriteString(StyleDim.Render(fmt.Sprintf("  ↓ %d more", len(m.rows)-end)) + "\n")
	}
	b.WriteString("\n" + StyleAccent.Render("j/k") + " move  " + StyleAccent.Render("enter") + " choose  " +
		StyleAccent.Render("K") + " all keep both  " + StyleAccent.Render("M") + " all mine  " +
		StyleAccent.Render("A") + " apply  " + StyleAccent.Render("esc") + " back")
	return b.String()
}

func (m *adoptModal) pickView(w int) string {
	r := m.rows[m.pickRow]
	label := "~/" + m.q.Rel
	if r.path != "" {
		label += "/" + r.path
	}
	var b strings.Builder
	b.WriteString("\n" + StyleAccent.Render(label) + "\n")
	for i, opt := range []string{"Keep both (recommended)", "This machine wins", "Back"} {
		cursor, text := "  ", opt
		if i == m.pickCur {
			cursor, text = StyleAccent.Render("> "), StyleAccent.Render(opt)
		}
		b.WriteString(cursor + fit(text, w-2) + "\n")
	}
	b.WriteString("\n" + StyleAccent.Render("j/k") + " move  " + StyleAccent.Render("enter") + " choose  " + StyleAccent.Render("esc") + " back")
	return b.String()
}

func (m *adoptModal) head() string {
	var b strings.Builder
	b.WriteString(StyleAccent.Render("Add ~/"+m.q.Rel) + "\n\n")
	b.WriteString("What each machine has:\n")
	for _, pm := range m.q.Report.Machines {
		name := pm.Name
		if pm.Local {
			name += " (here)"
		}
		b.WriteString("  " + fit(name, 22) + " " + adoptMachineState(m.q.Report, pm) + "\n")
	}
	if n := m.q.Report.Summary.OnlyThere; n > 0 {
		b.WriteString(StyleDim.Render(fmt.Sprintf("  %d %s only on other machines will be kept and spread", n, engine.Plural(n, "file", "files"))) + "\n")
	}
	if n := m.q.Report.Summary.Unknown; n > 0 {
		b.WriteString(StyleWarn.Render(fmt.Sprintf("  %d machine(s) could not be checked; the choice applies when they reconnect", n)) + "\n")
	}
	if n := m.q.Report.Summary.Truncated; n > 0 {
		b.WriteString(StyleWarn.Render(fmt.Sprintf("  %d machine(s) have too many files to compare; only keep-both is fully safe", n)) + "\n")
	}
	return b.String()
}

func adoptMachineState(r engine.AdoptionReport, m engine.ProbeMachine) string {
	switch {
	case m.Error != "":
		return StyleWarn.Render(m.Error)
	case !m.Exists:
		return "does not have it"
	case m.Entry:
		return "already synced in group " + m.EntryGroupLabel()
	case r.Dir:
		return fmt.Sprintf("%d files", len(m.Files))
	case len(m.Files) == 1 && len(r.Local.Files) == 1:
		f, local := m.Files[0], r.Local.Files[0]
		same := f.Hash == local.Hash && f.Exec == local.Exec
		if same {
			return "same · " + engine.FormatSize(f.Size)
		}
		return StyleWarn.Render("different") + " · " + engine.FormatSize(f.Size)
	}
	return "unknown"
}
