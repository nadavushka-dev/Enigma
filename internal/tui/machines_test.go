package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

// groupFake is a fake with three members and two ssh hosts.
func groupFake(t *testing.T) *fake {
	f := newFake(t)
	f.machines = []proto.MachineRecord{
		{ID: "id-dev", Name: "devbox", Alias: "devbox", Target: "ssh://me@devbox:22"},
		{ID: "id-mac", Name: "mac"},
		{ID: "id-lap", Name: "laptop", Alias: "lap", Target: "ssh://me@laptop:22"},
		{ID: "id-nas", Name: "nas"},
	}
	f.status.Peers = []daemon.PeerReport{
		{Name: "devbox", ID: "id-dev", Target: "ssh://me@devbox:22", Connected: true},
		{Name: "laptop", ID: "id-lap", Target: "ssh://me@laptop:22", Connected: false, LastError: "connection refused"},
		{Name: "nas", ID: "id-nas", Connected: false},
	}
	f.hosts = []string{"devbox", "lap", "office"}
	return f
}

func paneLines(m *Model) []string {
	p := m.panes[0]
	return strings.Split(p.View(m.s, 40, 12, true), "\n")
}

func TestMachinesRowsAndStates(t *testing.T) {
	m := started(t, groupFake(t))
	lines := paneLines(m)
	wants := []string{"● mac  this", "● devbox  linked", "○ laptop  down", "○ nas  dials us", "· office  ssh only"}
	for i, w := range wants {
		if !strings.Contains(lines[i], w) {
			t.Fatalf("row %d = %q, want %q", i, lines[i], w)
		}
	}
	v := m.View()
	for _, member := range []string{"devbox  ssh only", "lap  ssh only"} {
		if strings.Contains(v, member) {
			t.Fatalf("member listed as a host: %q", member)
		}
	}
	if !strings.Contains(v, "office  ssh only") {
		t.Fatal("office host missing")
	}
}

func TestMachinesDetailLine(t *testing.T) {
	m := started(t, groupFake(t))
	p := m.panes[0]
	drive(t, m, key("j"), key("j"))
	v := p.View(m.s, 60, 12, true)
	for _, w := range []string{"ssh://me@laptop:22", "connection refused"} {
		if !strings.Contains(v, w) {
			t.Fatalf("detail missing %q:\n%s", w, v)
		}
	}
	drive(t, m, key("G"))
	viewHas(t, m, "a: add to the group")
}

func TestMachinesAddHostRunsAddMachine(t *testing.T) {
	f := groupFake(t)
	f.steps = []string{"1. detect the platform", "2. install"}
	m := started(t, f)
	drive(t, m, key("G"), key("a"))
	viewHas(t, m, "Adding office", "1. detect the platform", "2. install", "done")
	calls := f.Calls()
	if len(calls) != 1 || calls[0] != "add-machine office" {
		t.Fatalf("calls: %v", calls)
	}
}

func TestMachinesAddMemberErrors(t *testing.T) {
	f := groupFake(t)
	m := started(t, f)
	drive(t, m, key("j"), key("a"))
	viewHas(t, m, "devbox is already in the group")
	if len(f.Calls()) != 0 {
		t.Fatalf("calls: %v", f.Calls())
	}
}

func TestMachinesRemoveMember(t *testing.T) {
	for _, tc := range []struct{ k, call string }{
		{"y", "remove-machine devbox uninstall=false"},
		{"u", "remove-machine devbox uninstall=true"},
		{"n", ""},
	} {
		f := groupFake(t)
		m := started(t, f)
		drive(t, m, key("j"), key("x"))
		viewHas(t, m, "Remove devbox from the group? Its files stay.", "remove and uninstall")
		drive(t, m, key(tc.k))
		calls := f.Calls()
		if tc.call == "" {
			if len(calls) != 0 {
				t.Fatalf("%s: calls %v", tc.k, calls)
			}
			continue
		}
		if len(calls) != 1 || calls[0] != tc.call {
			t.Fatalf("%s: calls %v, want %q", tc.k, calls, tc.call)
		}
		viewHas(t, m, "Removing devbox")
	}
}

func TestMachinesRemoveSelfErrorsAndHostIgnored(t *testing.T) {
	f := groupFake(t)
	m := started(t, f)
	drive(t, m, key("x"))
	viewHas(t, m, "this is this machine: remove it from another member")
	drive(t, m, key("G"), key("x"))
	if m.modal != nil || len(f.Calls()) != 0 {
		t.Fatal("x on a host must do nothing")
	}
}

func TestMachinesCursorSurvivesRefresh(t *testing.T) {
	f := groupFake(t)
	m := started(t, f)
	drive(t, m, key("j"), key("j")) // laptop
	f.mu.Lock()
	f.machines = f.machines[1:] // devbox gone
	f.mu.Unlock()
	drive(t, m, RefreshMsg{})
	sel := m.panes[0].(*machinesPane)
	if sel.rows[sel.cur].name != "laptop" {
		t.Fatalf("cursor on %q, want laptop", sel.rows[sel.cur].name)
	}
	f.mu.Lock()
	f.machines = f.machines[:1]
	f.mu.Unlock()
	drive(t, m, RefreshMsg{})
	if sel.cur >= len(sel.rows) {
		t.Fatalf("cursor %d past %d rows", sel.cur, len(sel.rows))
	}
}

func TestMachinesMovementKeys(t *testing.T) {
	m := started(t, groupFake(t))
	p := m.panes[0].(*machinesPane)
	drive(t, m, key("G"))
	if p.cur != 4 {
		t.Fatalf("G: %d", p.cur)
	}
	drive(t, m, key("k"), key("up"))
	if p.cur != 2 {
		t.Fatalf("k/up: %d", p.cur)
	}
	drive(t, m, key("g"), key("ctrl+d"))
	if p.cur == 0 {
		t.Fatal("ctrl+d did not move")
	}
	drive(t, m, key("ctrl+u"))
	if p.cur != 0 {
		t.Fatalf("ctrl+u: %d", p.cur)
	}
	drive(t, m, key("down"))
	if p.cur != 1 {
		t.Fatalf("down: %d", p.cur)
	}
}

func TestMachinesViewSizes(t *testing.T) {
	m := started(t, groupFake(t))
	p := m.panes[0]
	for _, sz := range [][2]int{{40, 12}, {10, 3}, {5, 1}, {30, 2}, {80, 40}} {
		for _, focused := range []bool{true, false} {
			v := p.View(m.s, sz[0], sz[1], focused)
			lines := strings.Split(v, "\n")
			if len(lines) != sz[1] {
				t.Fatalf("%v: %d lines", sz, len(lines))
			}
			for _, l := range lines {
				if lipgloss.Width(l) > sz[0] {
					t.Fatalf("%v: line %q too wide", sz, l)
				}
			}
		}
	}
	// a tiny pane still keeps the cursor visible
	drive(t, m, key("G"))
	if !strings.Contains(p.View(m.s, 40, 2, true), "office") {
		t.Fatal("cursor row scrolled out of view")
	}
}

func TestMachinesHelpListsAX(t *testing.T) {
	keys := map[string]bool{}
	for _, h := range newMachines().Help() {
		keys[h.Key] = true
	}
	if !keys["a"] || !keys["x"] {
		t.Fatalf("help: %v", keys)
	}
}
