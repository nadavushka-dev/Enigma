package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/enigma/internal/engine"
)

var t0 = time.Date(2026, 10, 20, 15, 4, 5, 0, time.Local)

func ev(seq uint64, kind, path, peer, detail string) engine.Event {
	return engine.Event{Seq: seq, Time: t0, Kind: kind, Path: path, Peer: peer, Detail: detail}
}

// plain is a rendered line without styling or padding.
func plain(s string) string {
	s = lipgloss.NewStyle().UnsetForeground().Render(s)
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			in = true
		case in && r == 'm':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

func TestActivityFormatsEachKind(t *testing.T) {
	cases := []struct {
		ev   engine.Event
		want string
	}{
		{ev(1, engine.EvReceived, "notes/a.md", "devbox", ""), "15:04:05 ← ~/notes/a.md from devbox"},
		{ev(2, engine.EvMerged, "notes/a.md", "devbox", ""), "15:04:05 ⇄ ~/notes/a.md merged with devbox"},
		{ev(3, engine.EvChanged, "notes/a.md", "", ""), "15:04:05 → ~/notes/a.md"},
		{ev(4, engine.EvDeleted, "notes/a.md", "", ""), "15:04:05 ✗ ~/notes/a.md"},
		{ev(5, engine.EvDeleted, "notes/a.md", "laptop", ""), "15:04:05 ✗ ~/notes/a.md by laptop"},
		{ev(6, engine.EvConflict, "notes/a (conflict).md", "", "notes/a.md"), "15:04:05 ! conflict: ~/notes/a.md (copy ~/notes/a (conflict).md)"},
		{ev(7, engine.EvEntryAdded, "notes", "", ""), "15:04:05 + ~/notes synced"},
		{ev(8, engine.EvEntryRemoved, "notes", "", ""), "15:04:05 - ~/notes no longer synced"},
		{ev(9, engine.EvLinkUp, "", "devbox", ""), "15:04:05 ● devbox linked"},
		{ev(10, engine.EvLinkDown, "", "devbox", ""), "15:04:05 ○ devbox down"},
		{ev(11, engine.EvUpgrade, "", "devbox", "v1.0.0 → v1.1.0"), "15:04:05 ↑ devbox: v1.0.0 → v1.1.0"},
		{ev(12, engine.EvError, "notes/a.md", "", "permission denied"), "15:04:05 notes/a.md: permission denied"},
		{ev(13, "weird", "p", "", "d"), "15:04:05 weird p d"},
	}
	for _, c := range cases {
		if got := plain(formatEvent(c.ev)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.ev.Kind, got, c.want)
		}
	}
}

func TestActivityStylesByKind(t *testing.T) {
	for _, c := range []struct {
		ev    engine.Event
		style lipgloss.Style
	}{
		{ev(1, engine.EvConflict, "c", "", "o"), StyleWarn},
		{ev(2, engine.EvLinkUp, "", "devbox", ""), StyleOK},
		{ev(3, engine.EvLinkDown, "", "devbox", ""), StyleDim},
		{ev(4, engine.EvError, "p", "", "d"), StyleBad},
	} {
		got := formatEvent(c.ev)
		if got != c.style.Render(plain(got)) {
			t.Errorf("%s: not styled as expected: %q", c.ev.Kind, got)
		}
	}
}

func TestActivityEmpty(t *testing.T) {
	m := started(t, newFake(t))
	viewHas(t, m, "no activity yet")
}

func TestActivityDedupesAndCaps(t *testing.T) {
	p := newActivity()
	s := &Shared{}
	var evs []engine.Event
	for i := 1; i <= 600; i++ {
		evs = append(evs, ev(uint64(i), engine.EvChanged, "f", "", ""))
	}
	p.Update(EventsMsg{Events: evs[:300]}, s)
	p.Update(EventsMsg{Events: evs[200:]}, s) // 200..300 again
	if len(p.events) != 500 {
		t.Fatalf("%d events, want 500", len(p.events))
	}
	if p.events[0].Seq != 101 || p.events[499].Seq != 600 {
		t.Fatalf("kept %d..%d, want 101..600", p.events[0].Seq, p.events[499].Seq)
	}
}

func TestActivityFollowsNewUnlessMovedUp(t *testing.T) {
	f := newFake(t)
	m := started(t, f)
	var evs []engine.Event
	for i := 1; i <= 20; i++ {
		evs = append(evs, ev(uint64(i), engine.EvChanged, "f"+itoa(i), "", ""))
	}
	drive(t, m, EventsMsg{Events: evs})
	viewHas(t, m, "~/f20")
	if strings.Contains(m.View(), "~/f1 ") {
		t.Fatal("the view must follow the newest events")
	}
	drive(t, m, key("tab"), key("tab")) // focus Activity
	drive(t, m, key("g"))
	viewHas(t, m, "~/f1 ")
	drive(t, m, EventsMsg{Events: []engine.Event{ev(21, engine.EvChanged, "f21", "", "")}})
	if !strings.Contains(m.View(), "~/f1 ") || strings.Contains(m.View(), "~/f21") {
		t.Fatal("a moved cursor must stay put when events arrive")
	}
	drive(t, m, key("G"))
	viewHas(t, m, "~/f21")
	drive(t, m, EventsMsg{Events: []engine.Event{ev(22, engine.EvChanged, "f22", "", "")}})
	viewHas(t, m, "~/f22")
}

func TestActivityMovementKeys(t *testing.T) {
	p := newActivity()
	s := &Shared{}
	var evs []engine.Event
	for i := 1; i <= 30; i++ {
		evs = append(evs, ev(uint64(i), engine.EvChanged, "f", "", ""))
	}
	p.Update(EventsMsg{Events: evs}, s)
	p.View(s, 60, 10, true)
	if p.cursor != 29 {
		t.Fatalf("cursor %d, want the newest", p.cursor)
	}
	steps := []struct {
		k    string
		want int
	}{{"k", 28}, {"up", 27}, {"ctrl+u", 22}, {"ctrl+d", 27}, {"j", 28}, {"down", 29}, {"j", 29}, {"g", 0}, {"k", 0}, {"G", 29}}
	for _, st := range steps {
		p.Update(key(st.k), s)
		if p.cursor != st.want {
			t.Fatalf("%s: cursor %d, want %d", st.k, p.cursor, st.want)
		}
	}
}

func TestActivityViewSizes(t *testing.T) {
	p := newActivity()
	s := &Shared{}
	for _, n := range []int{0, 3, 50} {
		var evs []engine.Event
		for i := 1; i <= n; i++ {
			evs = append(evs, ev(uint64(i), engine.EvConflict, "a very long path/that goes on/and on.md", "", "orig/and on.md"))
		}
		p.Update(EventsMsg{Events: evs}, s)
		for _, sz := range [][2]int{{10, 3}, {1, 1}, {0, 0}, {80, 5}, {200, 40}} {
			for _, focused := range []bool{true, false} {
				v := p.View(s, sz[0], sz[1], focused)
				lines := strings.Split(v, "\n")
				if sz[1] == 0 {
					if v != "" {
						t.Fatalf("height 0: %q", v)
					}
					continue
				}
				if len(lines) != sz[1] {
					t.Fatalf("n=%d %dx%d: %d lines", n, sz[0], sz[1], len(lines))
				}
				for _, l := range lines {
					if lipgloss.Width(l) > sz[0] {
						t.Fatalf("n=%d %dx%d: line %q too wide", n, sz[0], sz[1], l)
					}
				}
			}
		}
	}
}

func TestActivityEnterOnConflictOpensViewer(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/a.md", "one\ntwo\nthree\n")
	f.write(t, "notes/a (conflict).md", "one\nTWO\nthree\nfour\n")
	m := started(t, f)
	drive(t, m, EventsMsg{Events: []engine.Event{
		ev(1, engine.EvChanged, "x", "", ""),
		ev(2, engine.EvConflict, "notes/a (conflict).md", "", "notes/a.md"),
	}})
	drive(t, m, key("tab"), key("tab"), key("enter"))
	vm, ok := m.modal.(*viewerModal)
	if !ok {
		t.Fatalf("modal %T, want the viewer", m.modal)
	}
	v := vm.v
	if v.Title != "~/notes/a.md" || v.Left.Title != "original ~/notes/a.md" || v.Right.Title != "conflict copy ~/notes/a (conflict).md" {
		t.Fatalf("titles: %q %q %q", v.Title, v.Left.Title, v.Right.Title)
	}
	if strings.Join(v.Left.Lines, "|") != "one|two|three" || strings.Join(v.Right.Lines, "|") != "one|TWO|three|four" {
		t.Fatalf("lines: %v %v", v.Left.Lines, v.Right.Lines)
	}
	wantL, wantR := []bool{false, true, false}, []bool{false, true, false, true}
	if !eqBools(v.Left.Marks, wantL) || !eqBools(v.Right.Marks, wantR) {
		t.Fatalf("marks: %v %v", v.Left.Marks, v.Right.Marks)
	}
	viewHas(t, m, "conflict copy")
}

func TestActivityEnterOnVanishedCopyShowsError(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/a.md", "one\n")
	m := started(t, f)
	drive(t, m, EventsMsg{Events: []engine.Event{ev(1, engine.EvConflict, "notes/a (conflict).md", "", "notes/a.md")}})
	drive(t, m, key("tab"), key("tab"), key("enter"))
	if m.modal != nil {
		t.Fatal("no viewer for a missing copy")
	}
	viewHas(t, m, "the conflict copy is gone")
}

func TestActivityEnterOnOtherLinesDoesNothing(t *testing.T) {
	f := newFake(t)
	m := started(t, f)
	drive(t, m, EventsMsg{Events: []engine.Event{ev(1, engine.EvChanged, "x", "", "")}})
	drive(t, m, key("tab"), key("tab"))
	_, cmd := m.Update(key("enter"))
	if cmd != nil || m.modal != nil {
		t.Fatal("enter on a plain line must do nothing")
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("calls: %v", f.Calls())
	}
}

func TestActivityHelpListsEnter(t *testing.T) {
	var found bool
	for _, h := range newActivity().Help() {
		if h.Key == "enter" {
			found = true
		}
	}
	if !found {
		t.Fatal("Help must list enter")
	}
}

func eqBools(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func itoa(i int) string { return strconv.Itoa(i) }

var _ tea.Msg = EventsMsg{}
