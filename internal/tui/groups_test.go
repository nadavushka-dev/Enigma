package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

// groupsFake: three members, main and a two-member group "pair".
func groupsFake(t *testing.T) *fake {
	f := fleetFake(t)
	f.groups = []engine.GroupInfo{
		{GroupRecord: proto.GroupRecord{ID: "main", Name: "main", All: true, Members: []string{"id-dev", "id-lap", "id-mac", "id-nas"}}, MemberNames: []string{"devbox", "laptop", "mac", "nas"}, Entries: 2},
		{GroupRecord: proto.GroupRecord{ID: "g-pair", Name: "pair", Members: []string{"id-dev", "id-mac"}}, MemberNames: []string{"devbox", "mac"}, Entries: 1},
	}
	f.status.Entries = []engine.EntryStatus{{Path: "notes", Dir: true, Group: "main"}, {Path: "CLAUDE.md", Group: "main"}, {Path: "secret", Dir: true, Group: "g-pair"}}
	return f
}

func groupsModel(t *testing.T, f *fake) *Model {
	t.Helper()
	m := started(t, f)
	m.focus = 1
	return m
}

func TestGroupsPaneListsGroupsWithMembersAndEntryCounts(t *testing.T) {
	m := groupsModel(t, groupsFake(t))
	v := m.panes[1].View(m.s, 50, 10, true)
	for _, want := range []string{"main", "devbox, laptop, mac, nas", "2", "pair", "devbox, mac", "1"} {
		if !strings.Contains(v, want) {
			t.Fatalf("groups pane missing %q:\n%s", want, v)
		}
	}
	viewHas(t, m, "Groups", "n new", "x delete", "M merge")
}

func TestGroupsCreateAsksNameThenMembers(t *testing.T) {
	f := groupsFake(t)
	m := groupsModel(t, f)
	drive(t, m, key("n"))
	viewHas(t, m, "New group", "name")
	typeText(t, m, "build")
	drive(t, m, key("enter"))
	viewHas(t, m, "Machines in build", "[ ] devbox", "[ ] laptop", "[ ] mac", "[ ] nas")
	drive(t, m, key("space"), key("j"), key("j"), key("space"), key("enter"))
	calls := f.Calls()
	if len(calls) != 1 || calls[0] != "group create build add=[id-dev id-mac]" {
		t.Fatalf("calls %v", calls)
	}
	if l := noticeLine(m); !strings.Contains(l, "created group build") {
		t.Fatalf("notice %q", l)
	}
}

func TestGroupsDeleteConfirmsAndShowsTheDaemonsRefusal(t *testing.T) {
	f := groupsFake(t)
	m := groupsModel(t, f)
	drive(t, m, key("x"))
	viewHas(t, m, "main cannot be deleted")
	drive(t, m, key("j"), key("x"))
	viewHas(t, m, "Delete group pair?")
	f.opErr = errors.New("group pair still has 1 entry; move or remove them first")
	drive(t, m, key("y"))
	viewHas(t, m, "still has 1 entry")
	f.opErr = nil
	drive(t, m, key("x"), key("y"))
	if c := f.Calls(); len(c) != 2 || c[1] != "group delete g-pair" {
		t.Fatalf("calls %v", c)
	}
}

func TestGroupsRenamePrompts(t *testing.T) {
	f := groupsFake(t)
	m := groupsModel(t, f)
	drive(t, m, key("j"), key("r"), key("ctrl+u"))
	typeText(t, m, "duo")
	drive(t, m, key("enter"))
	if c := f.Calls(); len(c) != 1 || c[0] != "group rename g-pair duo" {
		t.Fatalf("calls %v", c)
	}
}

func TestGroupsEnterEditsMembers(t *testing.T) {
	f := groupsFake(t)
	m := groupsModel(t, f)
	drive(t, m, key("j"), key("enter"))
	viewHas(t, m, "Machines in pair", "[x] devbox", "[ ] laptop", "[x] mac", "[ ] nas")
	// uncheck devbox, check laptop
	drive(t, m, key("space"), key("j"), key("space"), key("enter"))
	viewHas(t, m, "laptop joins pair", "devbox leaves pair")
	drive(t, m, key("y"))
	if c := f.Calls(); len(c) != 1 || c[0] != "group members g-pair add=[id-lap] remove=[id-dev]" {
		t.Fatalf("calls %v", c)
	}
}

func TestGroupsMergeShowsThePlan(t *testing.T) {
	f := groupsFake(t)
	m := groupsModel(t, f)
	drive(t, m, key("j"), key("M"))
	viewHas(t, m, "Merge pair into")
	typeText(t, m, "main")
	drive(t, m, key("enter"))
	viewHas(t, m, "laptop gains ~/secret", "nas gains ~/secret", "then pair is deleted")
	drive(t, m, key("y"))
	c := f.Calls()
	if strings.Join(c, "; ") != "move "+filepath.Join(f.home, "secret")+" main; group delete g-pair" {
		t.Fatalf("calls %v", c)
	}
}

func TestFilesRowsShowTheGroupAndFrozenEntries(t *testing.T) {
	f := groupsFake(t)
	f.write(t, "notes/todo.md", "x")
	f.write(t, "secret/x.md", "x")
	f.write(t, "frozen/x.md", "x")
	f.status.Entries = append(f.status.Entries, engine.EntryStatus{Path: "frozen", Dir: true, Group: "g-pair", Frozen: true})
	m := filesModel(t, f)
	v := m.panes[2].View(m.s, 60, 10, true)
	for _, want := range []string{"synced", "synced · pair", "frozen · pair"} {
		if !strings.Contains(v, want) {
			t.Fatalf("files pane missing %q:\n%s", want, v)
		}
	}
}

// The Groups pane cursor browses; `a` makes the group under it the selected
// group, where adds and moves go (SPEC §13.2).
func TestGroupsAChoosesTheSelectedGroupShownInBothPanes(t *testing.T) {
	f := groupsFake(t)
	m := groupsModel(t, f)
	viewHas(t, m, "Files ~/ · adding to main")
	gv := m.panes[paneGroups].View(m.s, 50, 10, false)
	if !strings.Contains(gv, "▸ main") || strings.Contains(gv, "▸ pair") {
		t.Fatalf("main should carry the marker:\n%s", gv)
	}
	drive(t, m, key("j")) // browsing alone changes nothing
	viewHas(t, m, "Files ~/ · adding to main")
	drive(t, m, key("a"))
	viewHas(t, m, "Files ~/ · adding to pair")
	if l := noticeLine(m); !strings.Contains(l, "adding to pair") {
		t.Fatalf("notice %q", l)
	}
	gv = m.panes[paneGroups].View(m.s, 50, 10, false)
	if !strings.Contains(gv, "▸ pair") || strings.Contains(gv, "▸ main") {
		t.Fatalf("pair should carry the marker:\n%s", gv)
	}
	drive(t, m, key("k")) // the cursor leaves, the selection stays
	viewHas(t, m, "adding to pair")
	drive(t, m, StatusMsg{Status: f.status, Machines: f.machines, Groups: f.groups})
	viewHas(t, m, "adding to pair")
	// the selected group vanishes: back to main
	drive(t, m, StatusMsg{Status: f.status, Machines: f.machines, Groups: f.groups[:1]})
	viewHas(t, m, "adding to main")
}

func TestFilesAddGoesToTheSelectedGroupWithoutAPrompt(t *testing.T) {
	f := groupsFake(t)
	f.write(t, "new/x.md", "x")
	m := groupsModel(t, f)
	drive(t, m, key("j"), key("a"), key("tab")) // select pair, focus Files
	drive(t, m, key("G"), key("k"))
	n := m.panes[paneFiles].(*filesPane).current()
	if n == nil || n.rel != "new" {
		t.Fatalf("cursor on %v", n)
	}
	drive(t, m, key("a"))
	if c := f.Calls(); len(c) != 1 || c[0] != "add "+filepath.Join(f.home, "new")+" g-pair" {
		t.Fatalf("calls %v", c)
	}
	if l := noticeLine(m); !strings.Contains(l, "added ~/new to pair") {
		t.Fatalf("notice %q", l)
	}
}

func TestFilesAddWithMainSelectedUsesTheDefaultGroup(t *testing.T) {
	f := groupsFake(t)
	f.write(t, "new/x.md", "x")
	m := filesModel(t, f)
	drive(t, m, key("G"), key("k"), key("a"))
	if c := f.Calls(); len(c) != 1 || c[0] != "add "+filepath.Join(f.home, "new") {
		t.Fatalf("calls %v", c)
	}
}

func TestFilesMoveGoesToTheSelectedGroup(t *testing.T) {
	f := groupsFake(t)
	f.write(t, "secret/x.md", "x")
	m := filesModel(t, f) // main selected; secret is in pair
	drive(t, m, key("m"))
	if c := f.Calls(); len(c) != 1 || c[0] != "move "+filepath.Join(f.home, "secret")+" main" {
		t.Fatalf("calls %v", c)
	}
	if l := noticeLine(m); !strings.Contains(l, "moved ~/secret to main") {
		t.Fatalf("notice %q", l)
	}
	// an entry already in the selected group: a notice, no call
	drive(t, m, key("shift+tab"), key("j"), key("a"), key("tab"), key("m"))
	if c := f.Calls(); len(c) != 1 {
		t.Fatalf("calls %v", c)
	}
	if l := noticeLine(m); !strings.Contains(l, "already in pair") {
		t.Fatalf("notice %q", l)
	}
}

func TestMachinesEnterEditsAMembersGroups(t *testing.T) {
	f := groupsFake(t)
	m := started(t, f)
	drive(t, m, key("j"), key("enter")) // devbox: in main and pair
	viewHas(t, m, "Groups of devbox", "[x] main", "[x] pair")
	drive(t, m, key("space"), key("enter")) // out of main
	viewHas(t, m, "devbox leaves main")
	drive(t, m, key("y"))
	if c := f.Calls(); len(c) != 1 || c[0] != "group members main add=[] remove=[id-dev]" {
		t.Fatalf("calls %v", c)
	}
}

func TestMachinesAddAsksForGroupsWhenThereAreSeveral(t *testing.T) {
	f := groupsFake(t)
	m := started(t, f)
	drive(t, m, key("G"), key("a")) // office, an ssh host
	viewHas(t, m, "Groups for office", "[x] main", "[ ] pair")
	drive(t, m, key("j"), key("space"), key("enter"))
	viewHas(t, m, "Adding office")
	if c := f.Calls(); len(c) != 1 || c[0] != "add-machine office groups=[main pair]" {
		t.Fatalf("calls %v", c)
	}
}
