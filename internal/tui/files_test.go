package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/enigma/internal/engine"
)

// filesModel is a Model with the Files pane focused over a small home.
func filesModel(t *testing.T, f *fake) *Model {
	t.Helper()
	m := started(t, f)
	drive(t, m, key("tab"))
	return m
}

func files(m *Model) *filesPane { return m.panes[1].(*filesPane) }

// rows is the pane's visible rows as plain "<rel>" strings.
func rows(m *Model) []string {
	var out []string
	for _, r := range files(m).rows {
		out = append(out, r.rel)
	}
	return out
}

func selected(t *testing.T, m *Model, want string) {
	t.Helper()
	p := files(m)
	if p.cursor >= len(p.rows) || p.rows[p.cursor].rel != want {
		t.Fatalf("selected %q, want %q (rows %v)", p.rows[p.cursor].rel, want, rows(m))
	}
}

func TestFilesTreeShowsDotfilesFoldersFirstSorted(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/todo.md", "x")
	f.write(t, ".claude/CLAUDE.md", "x")
	f.write(t, "b.txt", "x")
	f.write(t, ".zshrc", "x")
	f.write(t, "Downloads/a", "x")
	m := filesModel(t, f)
	got := strings.Join(rows(m), " ")
	want := ".claude Downloads notes .zshrc b.txt"
	if got != want {
		t.Fatalf("rows %q, want %q", got, want)
	}
	viewHas(t, m, "Files ~/", ".claude/", "Downloads/", "notes/", ".zshrc", "b.txt")
	if strings.Contains(m.View(), "todo.md") {
		t.Fatal("folders are not read before they are expanded")
	}
}

func TestFilesExpandCollapseAndParent(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/sub/deep.md", "x")
	f.write(t, "notes/todo.md", "x")
	f.write(t, "a.txt", "x")
	m := filesModel(t, f)
	selected(t, m, "notes")
	drive(t, m, key("l"))
	if got := strings.Join(rows(m), " "); got != "notes notes/sub notes/todo.md a.txt" {
		t.Fatalf("after expand: %q", got)
	}
	viewHas(t, m, "  sub/", "  todo.md")
	selected(t, m, "notes")
	drive(t, m, key("l")) // expanded already: into the first child
	selected(t, m, "notes/sub")
	drive(t, m, key("enter"))
	selected(t, m, "notes/sub")
	drive(t, m, key("j"))
	selected(t, m, "notes/sub/deep.md")
	drive(t, m, key("l")) // a file: nothing
	selected(t, m, "notes/sub/deep.md")
	drive(t, m, key("h")) // a file: to the parent
	selected(t, m, "notes/sub")
	drive(t, m, key("h")) // expanded folder: collapse
	selected(t, m, "notes/sub")
	if got := strings.Join(rows(m), " "); got != "notes notes/sub notes/todo.md a.txt" {
		t.Fatalf("after collapse: %q", got)
	}
	drive(t, m, key("left")) // collapsed folder: to the parent
	selected(t, m, "notes")
	drive(t, m, key("h"))
	if got := strings.Join(rows(m), " "); got != "notes a.txt" {
		t.Fatalf("after collapsing notes: %q", got)
	}
	drive(t, m, key("h")) // at the top level: stays
	selected(t, m, "notes")
	drive(t, m, key("right"))
	selected(t, m, "notes")
	if len(rows(m)) != 4 {
		t.Fatalf("right expands: %v", rows(m))
	}
}

func TestFilesMarks(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/todo.md", "x")
	f.write(t, "notes/big.bin", "x")
	f.write(t, "gone/x", "x")
	f.write(t, "CLAUDE.md", "x")
	f.write(t, "plain.txt", "x")
	f.status.Entries = []engine.EntryStatus{
		{Path: "notes", Dir: true, Skipped: []string{"notes/big.bin"}},
		{Path: "gone", Dir: true, Missing: true},
		{Path: "CLAUDE.md"},
	}
	m := filesModel(t, f)
	drive(t, m, m.fetchStatus()())
	p := files(m)
	marks := map[string]string{}
	for _, r := range p.rows {
		marks[r.rel] = p.mark(m.s, r)
	}
	drive(t, m, key("j"), key("l")) // gone/, then notes/
	selected(t, m, "notes")
	for _, r := range p.rows {
		marks[r.rel] = p.mark(m.s, r)
	}
	want := map[string]string{"notes": "synced", "gone": "missing here", "CLAUDE.md": "synced",
		"notes/todo.md": "·", "notes/big.bin": "skipped", "plain.txt": ""}
	for rel, w := range want {
		if marks[rel] != w {
			t.Fatalf("mark for %s: %q, want %q", rel, marks[rel], w)
		}
	}
	v := m.View()
	for _, w := range []string{"synced", "missing here", "skipped", "·"} {
		if !strings.Contains(v, w) {
			t.Fatalf("view lacks %q:\n%s", w, v)
		}
	}
	// marks are right-aligned: "synced" ends the row's text
	for _, l := range strings.Split(v, "\n") {
		if strings.Contains(l, "CLAUDE.md") {
			plain := strings.TrimRight(stripANSI(l), "│ ")
			if !strings.HasSuffix(plain, "synced") {
				t.Fatalf("mark not right-aligned: %q", plain)
			}
		}
	}
}

func TestFilesDimming(t *testing.T) {
	f := newFake(t)
	f.write(t, "proj/.git/HEAD", "x")
	f.write(t, "proj/node_modules/m.js", "x")
	f.write(t, "proj/src/main.go", "x")
	f.write(t, "proj/.DS_Store", "x")
	big := filepath.Join(f.home, "proj", "big.bin")
	if err := os.WriteFile(big, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, engine.DefaultMaxFileSize+1); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.home, "proj", "src"), filepath.Join(f.home, "proj", "link")); err != nil {
		t.Fatal(err)
	}
	m := filesModel(t, f)
	drive(t, m, key("l"))
	p := files(m)
	dim := map[string]bool{}
	for _, r := range p.rows {
		dim[r.rel] = p.dimmed(m.s, r)
	}
	for rel, w := range map[string]bool{"proj": false, "proj/.git": true, "proj/node_modules": true,
		"proj/src": false, "proj/.DS_Store": true, "proj/big.bin": true, "proj/link": true} {
		if dim[rel] != w {
			t.Fatalf("dimmed(%s)=%v, want %v", rel, dim[rel], w)
		}
	}
	viewHas(t, m, "link -> ")
	// the symlinked folder is never expanded
	for p.rows[p.cursor].rel != "proj/link" {
		drive(t, m, key("j"))
	}
	n := len(p.rows)
	drive(t, m, key("l"))
	if len(p.rows) != n || strings.Contains(m.View(), "main.go") {
		t.Fatal("symlink expanded")
	}
	// inside an entry, ignores are relative to the entry root
	f.write(t, "proj/.git/HEAD", "x")
	f.status.Entries = []engine.EntryStatus{{Path: "proj", Dir: true}}
	drive(t, m, m.fetchStatus()())
	for _, r := range p.rows {
		if r.rel == "proj/.git" && !p.dimmed(m.s, r) {
			t.Fatal(".git inside an entry must stay dimmed")
		}
		if r.rel == "proj/src" && p.dimmed(m.s, r) {
			t.Fatal("a synced folder's child must not be dimmed")
		}
	}
}

func TestFilesAddCallsBackend(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/todo.md", "x")
	f.write(t, "CLAUDE.md", "x")
	m := filesModel(t, f)
	drive(t, m, key("a"))
	want := "add " + filepath.Join(f.home, "notes")
	if c := f.Calls(); len(c) != 1 || c[0] != want {
		t.Fatalf("calls %v, want [%s]", c, want)
	}
	f.opErr = errors.New("daemon says no")
	drive(t, m, key("j"), key("a"))
	viewHas(t, m, "daemon says no")
	if len(f.Calls()) != 2 {
		t.Fatalf("calls %v", f.Calls())
	}
}

func TestFilesAddRefusesAlreadySynced(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/todo.md", "x")
	f.status.Entries = []engine.EntryStatus{{Path: "notes", Dir: true}}
	m := filesModel(t, f)
	drive(t, m, key("a"))
	viewHas(t, m, "~/notes is already synced")
	drive(t, m, key("l"), key("j"), key("a"))
	viewHas(t, m, "~/notes/todo.md is already synced")
	if len(f.Calls()) != 0 {
		t.Fatalf("Add called: %v", f.Calls())
	}
}

func TestFilesRemoveConfirmsThenCallsBackend(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/todo.md", "x")
	f.write(t, "a.txt", "x")
	f.status.Entries = []engine.EntryStatus{{Path: "notes", Dir: true}}
	m := filesModel(t, f)
	drive(t, m, key("x"))
	viewHas(t, m, "Stop syncing ~/notes on every machine? The files stay.")
	drive(t, m, key("n"))
	if len(f.Calls()) != 0 {
		t.Fatal("n must not remove")
	}
	drive(t, m, key("x"))
	drive(t, m, key("y"))
	want := "remove " + filepath.Join(f.home, "notes")
	if c := f.Calls(); len(c) != 1 || c[0] != want {
		t.Fatalf("calls %v, want [%s]", c, want)
	}
	drive(t, m, key("j"), key("x"))
	viewHas(t, m, "~/a.txt is not a synced entry")
	if m.modal != nil {
		t.Fatal("no dialog for a non-entry")
	}
	f.opErr = errors.New("remove failed")
	drive(t, m, key("k"), key("x"))
	drive(t, m, key("y"))
	viewHas(t, m, "remove failed")
}

func TestFilesSelectionSurvivesStatus(t *testing.T) {
	f := newFake(t)
	for _, n := range []string{"a", "b", "c"} {
		f.write(t, n+".txt", "x")
	}
	m := filesModel(t, f)
	drive(t, m, key("j"), key("j"))
	selected(t, m, "c.txt")
	drive(t, m, m.fetchStatus()())
	selected(t, m, "c.txt")
}

func TestFilesMovementAndScrolling(t *testing.T) {
	f := newFake(t)
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		f.write(t, n+".txt", "x")
	}
	m := filesModel(t, f)
	p := files(m)
	drive(t, m, key("G"))
	selected(t, m, "h.txt")
	drive(t, m, key("g"))
	selected(t, m, "a.txt")
	drive(t, m, key("down"), key("down"), key("up"))
	selected(t, m, "b.txt")
	drive(t, m, key("ctrl+d"))
	if p.cursor <= 1 {
		t.Fatalf("ctrl+d: cursor %d", p.cursor)
	}
	drive(t, m, key("ctrl+u"))
	selected(t, m, "b.txt")
	drive(t, m, key("k"), key("k"))
	selected(t, m, "a.txt")
	// a 3-line pane shows the selected row
	drive(t, m, key("G"))
	v := p.View(m.s, 30, 3, true)
	if lines := strings.Split(v, "\n"); len(lines) != 3 || !strings.Contains(v, "h.txt") {
		t.Fatalf("cursor not visible:\n%s", v)
	}
	drive(t, m, key("g"))
	v = p.View(m.s, 30, 3, true)
	if !strings.Contains(v, "a.txt") || strings.Contains(v, "h.txt") {
		t.Fatalf("scroll back:\n%s", v)
	}
	if u := p.View(m.s, 30, 3, false); !strings.Contains(u, "a.txt") {
		t.Fatalf("unfocused view:\n%s", u)
	}
}

func TestFilesViewSizes(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/todo.md", "x")
	f.write(t, "a-very-long-file-name-that-does-not-fit.txt", "x")
	f.status.Entries = []engine.EntryStatus{{Path: "notes", Dir: true}}
	m := filesModel(t, f)
	p := files(m)
	for _, sz := range [][2]int{{10, 3}, {1, 1}, {0, 0}, {40, 1}, {80, 20}, {5, 8}} {
		v := p.View(m.s, sz[0], sz[1], true)
		lines := strings.Split(v, "\n")
		if sz[1] == 0 {
			if v != "" {
				t.Fatalf("height 0: %q", v)
			}
			continue
		}
		if len(lines) != sz[1] {
			t.Fatalf("%v: %d lines", sz, len(lines))
		}
		for _, l := range lines {
			if lipgloss.Width(l) > sz[0] {
				t.Fatalf("%v: line %q too wide", sz, l)
			}
		}
	}
	var _ = tea.KeyMsg{}
}

func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			in = true
		case in && r == 'm':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// An ignored item inside a synced folder does not sync: no "inside a
// synced folder" dot, and dimmed.
func TestFilesIgnoredInsideAnEntryIsNotMarkedAsSyncing(t *testing.T) {
	f := newFake(t)
	f.write(t, "proj/a.md", "x")
	f.write(t, "proj/.git/HEAD", "x")
	f.status.Entries = []engine.EntryStatus{{Path: "proj", Dir: true}}
	m := filesModel(t, f)
	p := files(m)
	drive(t, m, key("l")) // open proj
	var git *fileNode
	for _, n := range p.rows {
		if n.rel == "proj/.git" {
			git = n
		}
	}
	if git == nil {
		t.Fatalf("rows %v", rows(m))
	}
	if mk := p.mark(m.s, git); mk != "" {
		t.Fatalf("ignored .git marked %q", mk)
	}
	if !p.dimmed(m.s, git) {
		t.Fatal("ignored .git inside an entry must be dimmed")
	}
}

// Files that appear while the TUI is open show up on the next status
// refresh; removed ones go; open folders stay open.
func TestFilesTreeFollowsTheDisk(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/a.md", "x")
	m := filesModel(t, f)
	drive(t, m, key("l")) // open notes
	f.write(t, "notes/b.md", "x")
	f.write(t, "new.txt", "x")
	os.Remove(filepath.Join(f.home, "notes/a.md"))
	drive(t, m, m.fetchStatus()())
	got := strings.Join(rows(m), " ")
	if got != "notes notes/b.md new.txt" {
		t.Fatalf("rows %q", got)
	}
}

// One drive call with a key that opens a dialog and the key answering it:
// the dialog must open first.
func TestDriveRunsEachMessageToTheEndBeforeTheNext(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/a.md", "x")
	f.status.Entries = []engine.EntryStatus{{Path: "notes", Dir: true}}
	m := filesModel(t, f)
	drive(t, m, key("x"), key("y"))
	if got := strings.Join(f.Calls(), ","); !strings.Contains(got, "remove "+filepath.Join(f.home, "notes")) {
		t.Fatalf("calls %q", got)
	}
}
