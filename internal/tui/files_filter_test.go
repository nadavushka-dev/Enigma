package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/fuzzy"
)

// filterHome is a Model over a home with a few nested folders, filter open.
func filterHome(t *testing.T) (*fake, *Model) {
	t.Helper()
	f := newFake(t)
	f.write(t, "notes/sub/deep.md", "x")
	f.write(t, "notes/todo.md", "x")
	f.write(t, "proj/src/main.go", "x")
	f.write(t, "proj/.git/HEAD", "x")
	f.write(t, "a.txt", "x")
	m := filesModel(t, f)
	return f, m
}

// hits is what the filter lists, in order.
func hits(m *Model) []string {
	var out []string
	for _, h := range files(m).filter.hits {
		out = append(out, h.Path)
	}
	return out
}

func typeKeys(t *testing.T, m *Model, s string) {
	t.Helper()
	for _, r := range s {
		drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func TestFilterTreeScopeListsTheFoldersOpenedSoFar(t *testing.T) {
	_, m := filterHome(t)
	drive(t, m, key("/"))
	p := files(m)
	if p.filter == nil {
		t.Fatal("/ opens the filter")
	}
	if got := hits(m); !reflect.DeepEqual(got, []string{"a.txt", "proj/", "notes/"}) { // ties: shorter first
		t.Fatalf("empty query lists every opened row: %v", got)
	}
	typeKeys(t, m, "td")
	if got := hits(m); !reflect.DeepEqual(got, []string{}) && len(got) != 0 {
		t.Fatalf("notes is not opened yet: %v", got)
	}
	viewHas(t, m, "/ td")
	drive(t, m, key("esc"))
	drive(t, m, key("l")) // open notes
	drive(t, m, key("/"))
	typeKeys(t, m, "td")
	if got := hits(m); !reflect.DeepEqual(got, []string{"notes/todo.md"}) {
		t.Fatalf("hits %v", got)
	}
	viewHas(t, m, "notes/todo.md")
	if strings.Contains(m.View(), "a.txt") {
		t.Fatal("rows are replaced by the matches")
	}
	drive(t, m, key("backspace"), key("backspace"))
	if p.filter.query != "" || len(hits(m)) != 5 {
		t.Fatalf("backspace: query %q, hits %v", p.filter.query, hits(m))
	}
}

func TestFilterTakesEveryKeyWhileOpen(t *testing.T) {
	_, m := filterHome(t)
	drive(t, m, key("/"))
	typeKeys(t, m, "q?")
	if files(m).filter == nil || files(m).filter.query != "q?" || m.help {
		t.Fatalf("q and ? must type: filter %+v help %v", files(m).filter, m.help)
	}
	drive(t, m, key("tab"))
	if m.focus != paneFiles {
		t.Fatal("tab switches the scope, not the focus")
	}
	_, cmd := m.Update(key("ctrl+c"))
	if cmd == nil {
		t.Fatal("ctrl+c still quits")
	}
}

func TestFilterHomeScopeUsesTheIndex(t *testing.T) {
	_, m := filterHome(t)
	drive(t, m, key("/"), key("tab"))
	p := files(m)
	if p.filter.scope != scopeHome {
		t.Fatalf("scope %v", p.filter.scope)
	}
	viewHas(t, m, "~")
	typeKeys(t, m, "deep")
	if got := hits(m); !reflect.DeepEqual(got, []string{"notes/sub/deep.md"}) {
		t.Fatalf("hits %v", got)
	}
	typeKeys(t, m, " zzz")
	if len(hits(m)) != 0 {
		t.Fatalf("hits %v", hits(m))
	}
	for i := 0; i < 4; i++ {
		drive(t, m, key("backspace"))
	}
	drive(t, m, key("tab")) // back to the tree
	if p.filter.scope != scopeTree || len(hits(m)) != 0 {
		t.Fatalf("scope %v hits %v", p.filter.scope, hits(m))
	}
}

func TestFilterStatusLine(t *testing.T) {
	cases := map[fuzzy.Progress]string{
		{Count: 12}:            "indexing… 12",
		{Count: 3, Done: true}: "",
		{Count: 50000, Done: true, Truncated: true}: "first 50000 paths",
	}
	for pr, want := range cases {
		if got := indexStatus(pr); got != want {
			t.Fatalf("%+v: %q, want %q", pr, got, want)
		}
	}
}

func TestFilterMovesAndEnterJumpsIntoTheTree(t *testing.T) {
	_, m := filterHome(t)
	drive(t, m, key("j"), key("j")) // a.txt
	selected(t, m, "a.txt")
	drive(t, m, key("/"), key("tab"))
	typeKeys(t, m, "md")
	if got := hits(m); !reflect.DeepEqual(got, []string{"notes/todo.md", "notes/sub/deep.md"}) {
		t.Fatalf("hits %v", got)
	}
	p := files(m)
	drive(t, m, key("down"))
	if p.filter.sel != 1 {
		t.Fatalf("down: sel %d", p.filter.sel)
	}
	drive(t, m, key("ctrl+n"), key("ctrl+p"), key("up"))
	if p.filter.sel != 0 {
		t.Fatalf("sel %d", p.filter.sel)
	}
	drive(t, m, key("up"))
	if p.filter.sel != 0 {
		t.Fatal("up stops at the top")
	}
	drive(t, m, key("enter"))
	if p.filter != nil {
		t.Fatal("enter closes the filter")
	}
	selected(t, m, "notes/todo.md")
	if got := strings.Join(rows(m), " "); got != "notes notes/sub notes/todo.md proj a.txt" {
		t.Fatalf("parents opened: %q", got)
	}
	// a and x work on the jumped-to row as usual
	drive(t, m, key("a"))
	if c := m.s.Backend.(*fake).Calls(); len(c) != 1 || !strings.HasSuffix(c[0], "notes/todo.md") {
		t.Fatalf("calls %v", c)
	}
	// a folder pick lands on the folder itself
	drive(t, m, key("/"), key("tab"))
	typeKeys(t, m, "sub/")
	drive(t, m, key("enter"))
	selected(t, m, "notes/sub")
}

func TestFilterEscRestoresTheCursor(t *testing.T) {
	_, m := filterHome(t)
	drive(t, m, key("l"), key("j"), key("j")) // notes/todo.md
	selected(t, m, "notes/todo.md")
	drive(t, m, key("/"))
	typeKeys(t, m, "a")
	drive(t, m, key("down"), key("esc"))
	if files(m).filter != nil {
		t.Fatal("esc closes the filter")
	}
	selected(t, m, "notes/todo.md")
	if got := strings.Join(rows(m), " "); got != "notes notes/sub notes/todo.md proj a.txt" {
		t.Fatalf("tree changed: %q", got)
	}
}

func TestFilterEnterOnNothingJustCloses(t *testing.T) {
	_, m := filterHome(t)
	drive(t, m, key("/"))
	typeKeys(t, m, "zzz")
	drive(t, m, key("enter"))
	if files(m).filter != nil {
		t.Fatal("filter still open")
	}
	selected(t, m, "notes")
}

func TestFilterHighlightsMatches(t *testing.T) {
	got := highlightSegments("notes/todo.md", []int{6, 7, 8, 9})
	want := []segment{{"notes/", false}, {"todo", true}, {".md", false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("segments %v", got)
	}
	got = highlightSegments("ab", []int{0, 1})
	if !reflect.DeepEqual(got, []segment{{"ab", true}}) {
		t.Fatalf("segments %v", got)
	}
	_, m := filterHome(t)
	drive(t, m, key("/"))
	typeKeys(t, m, "a")
	if v := m.View(); !strings.Contains(v, styleMatch.Render("a")) {
		t.Fatalf("no highlight in\n%s", v)
	}
}

func TestFilterHelpAndFooter(t *testing.T) {
	_, m := filterHome(t)
	viewHas(t, m, "/ filter")
	drive(t, m, key("?"))
	viewHas(t, m, "scope", "jump to it in the tree")
	drive(t, m, key("j"), key("/"))
	viewHas(t, m, "esc cancel", "enter jump", "tab scope")
	if strings.Contains(m.View(), "q quit") {
		t.Fatal("q types while filtering")
	}
}

func TestFilterViewSizes(t *testing.T) {
	_, m := filterHome(t)
	drive(t, m, key("/"), key("tab"))
	typeKeys(t, m, "a")
	p := files(m)
	for _, sz := range [][2]int{{10, 3}, {1, 1}, {0, 0}, {40, 1}, {80, 20}, {5, 8}, {30, 2}} {
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
	// the selection stays visible in a short list
	for i := 0; i < 5; i++ {
		drive(t, m, key("down"))
	}
	v := p.View(m.s, 40, 3, true)
	if !strings.Contains(v, p.filter.hits[p.filter.sel].Path) {
		t.Fatalf("selected hit not visible:\n%s", v)
	}
}

// stuckIndex is a home index that never finishes.
type stuckIndex struct{ done bool }

func (s *stuckIndex) Snapshot() ([]fuzzy.Entry, fuzzy.Progress) {
	return []fuzzy.Entry{{Path: "a.txt"}}, fuzzy.Progress{Count: 1, Done: s.done}
}

// ticksIn runs cmd and counts the index ticks it yields (ranking results
// are dropped on the floor).
func ticksIn(cmd tea.Cmd) int {
	n := 0
	for _, msg := range run(cmd) {
		if _, ok := msg.(indexTickMsg); ok {
			n++
		}
	}
	return n
}

func TestFilterKeepsOneTickChainWhileIndexing(t *testing.T) {
	_, m := filterHome(t)
	drive(t, m, key("/"))
	p := files(m)
	ix := &stuckIndex{}
	p.filter.index = ix
	ticks := 0
	for _, k := range []string{"tab", "a", "b", "backspace"} {
		ticks += ticksIn(p.Update(key(k), m.s))
	}
	if ticks != 1 {
		t.Fatalf("%d pending ticks, want 1", ticks)
	}
	viewHas(t, m, "indexing… 1")
	if ticksIn(p.Update(indexTickMsg{}, m.s)) != 1 {
		t.Fatal("the tick re-arms itself while indexing")
	}
	if ticksIn(p.Update(key("c"), m.s)) != 0 {
		t.Fatal("a key must not start a second chain")
	}
	ix.done = true
	if ticksIn(p.Update(indexTickMsg{}, m.s)) != 0 {
		t.Fatal("the chain ends when the index is done")
	}
	if ticksIn(p.Update(key("d"), m.s)) != 0 {
		t.Fatal("no tick after done")
	}
}

// Ranking runs in a command; a result from an older query is dropped.
func TestFilterRanksInACommandAndDropsStaleResults(t *testing.T) {
	_, m := filterHome(t)
	drive(t, m, key("/"))
	p := files(m)
	cmdA := p.Update(key("a"), m.s)
	cmdN := p.Update(key("backspace"), m.s)
	if cmdA == nil || cmdN == nil {
		t.Fatal("typing must return the ranking command")
	}
	before := hits(m)
	drive(t, m, cmdA()) // the stale result for "a"
	if got := hits(m); !reflect.DeepEqual(got, before) {
		t.Fatalf("stale result applied: %v", got)
	}
	drive(t, m, cmdN())
	if got := hits(m); len(got) != 3 {
		t.Fatalf("current result: %v", got)
	}
	drive(t, m, key("esc"))
	drive(t, m, cmdN()) // a result after the filter closed is ignored
	if p.filter != nil {
		t.Fatal("filter reopened")
	}
}

func TestFilterSpaceResetsTheSelection(t *testing.T) {
	_, m := filterHome(t)
	drive(t, m, key("/"), key("down"), key("down"))
	if files(m).filter.sel != 2 {
		t.Fatalf("sel %d", files(m).filter.sel)
	}
	drive(t, m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if f := files(m).filter; f.sel != 0 || f.query != " " {
		t.Fatalf("sel %d query %q", f.sel, f.query)
	}
}

func TestFilterRowsShowTheSyncedMarks(t *testing.T) {
	f, m := filterHome(t)
	f.status.Entries = []engine.EntryStatus{{Path: "notes", Dir: true, Skipped: []string{"notes/sub/deep.md"}}, {Path: "proj/src/main.go"}}
	drive(t, m, m.fetchStatus()())
	drive(t, m, key("/"), key("tab"))
	v := stripANSI(m.View())
	for _, want := range []string{"notes/", "synced", "todo.md", "·", "deep.md", "skipped", "proj/", "contains synced"} {
		if !strings.Contains(v, want) {
			t.Fatalf("view lacks %q:\n%s", want, v)
		}
	}
	for _, l := range strings.Split(v, "\n") {
		plain := strings.TrimRight(l, "│ ")
		switch {
		case strings.Contains(l, " notes/ "):
			if !strings.HasSuffix(plain, "synced") {
				t.Fatalf("mark not right-aligned: %q", plain)
			}
		case strings.Contains(l, "deep.md"):
			if !strings.HasSuffix(plain, "skipped") {
				t.Fatalf("deep.md: %q", plain)
			}
		case strings.Contains(l, "main.go"):
			if !strings.HasSuffix(plain, "synced") {
				t.Fatalf("main.go: %q", plain)
			}
		}
	}
}
