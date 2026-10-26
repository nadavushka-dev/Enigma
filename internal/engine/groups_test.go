package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/proto"
)

func (m *machine) group(id string) (GroupInfo, bool) {
	for _, g := range m.e.Groups() {
		if g.ID == id {
			return g, true
		}
	}
	return GroupInfo{}, false
}

func TestD19_TheFirstMachineCreatesMainWithItself(t *testing.T) {
	a := newMachine(t, "a")
	g, ok := a.group(proto.MainGroup)
	if !ok || g.Name != "main" || strings.Join(g.Members, ",") != "id-a" || g.Removed {
		t.Fatalf("main: %+v %v", g, ok)
	}
}

func TestD19_UpgradeFromV02PutsEveryMemberAndEntryInMain(t *testing.T) {
	a := newMachine(t, "a")
	a.write("notes/x.md", "x")
	a.add("notes")
	if err := a.e.SetMachine(proto.MachineRecord{ID: "id-b", Name: "b"}); err != nil {
		t.Fatal(err)
	}
	a.e.Close()
	// A v0.2 state file: no groups, entries without a group.
	p := filepath.Join(a.data, "state.json")
	var st map[string]json.RawMessage
	b, _ := os.ReadFile(p)
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	delete(st, "groups")
	var entries map[string]map[string]any
	json.Unmarshal(st["entries"], &entries)
	for _, e := range entries {
		delete(e, "group")
	}
	st["entries"], _ = json.Marshal(entries)
	b, _ = json.Marshal(st)
	os.WriteFile(p, b, 0o600)
	a.start()

	g, ok := a.group(proto.MainGroup)
	if !ok || strings.Join(g.Members, ",") != "id-a,id-b" {
		t.Fatalf("main after upgrade: %+v %v", g, ok)
	}
	if e := a.e.Entries()[0]; e.Path != "notes" || e.Group != proto.MainGroup {
		t.Fatalf("entry after upgrade: %+v", e)
	}
}

func TestD19_GroupRecordsReplicateAndTheLatestWins(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	defer link(t, a, b)()
	eventually(t, "a knows b", func() bool { _, ok := a.machine("id-b"); return ok })
	id, err := a.e.CreateGroup("pair", []string{"id-a", "id-b"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "b sees pair", func() bool {
		g, ok := b.group(id)
		return ok && g.Name == "pair" && strings.Join(g.Members, ",") == "id-a,id-b"
	})
	if err := b.e.RenameGroup(id, "duo"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a sees the rename", func() bool { g, _ := a.group(id); return g.Name == "duo" })
	// main carries both machines once b is known to a (machine add does that).
	if err := a.e.GroupMembers(proto.MainGroup, []string{"id-b"}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b is in main everywhere", func() bool { g, _ := b.group(proto.MainGroup); return strings.Join(g.Members, ",") == "id-a,id-b" })
	if err := b.e.GroupMembers(proto.MainGroup, nil, []string{"id-b"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b left main", func() bool { g, _ := a.group(proto.MainGroup); return strings.Join(g.Members, ",") == "id-a" })
}

func TestD19_MainCannotBeDeletedOrRenamedAndNamesAreChecked(t *testing.T) {
	a := newMachine(t, "a")
	if err := a.e.DeleteGroup(proto.MainGroup); err == nil || !strings.Contains(err.Error(), "main") {
		t.Fatalf("delete main: %v", err)
	}
	if err := a.e.RenameGroup(proto.MainGroup, "x"); err == nil {
		t.Fatal("rename main")
	}
	for _, bad := range []string{"", "main", "has space", "a/b"} {
		if _, err := a.e.CreateGroup(bad, nil); err == nil {
			t.Fatalf("created %q", bad)
		}
	}
	if _, err := a.e.CreateGroup("pair", []string{"id-nobody"}); err == nil || !strings.Contains(err.Error(), "id-nobody") {
		t.Fatalf("unknown member: %v", err)
	}
}

// Deleting a group stops syncing its entries: the files stay on every
// machine, the entries and the group become tombstones everywhere (SPEC §9).
func TestD19_DeleteStopsSyncingTheGroupsEntriesAndKeepsTheFiles(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	defer link(t, a, b)()
	eventually(t, "a knows b", func() bool { _, ok := a.machine("id-b"); return ok })
	id, err := a.e.CreateGroup("pair", []string{"id-a", "id-b"})
	if err != nil {
		t.Fatal(err)
	}
	a.write("p/x", "x")
	if err := a.e.Add(a.path("p"), nil, id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b has the file", func() bool { return b.read("p/x") == "x" })
	if err := a.e.DeleteGroup(id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b forgets pair", func() bool { _, ok := b.group(id); return !ok })
	eventually(t, "b stops syncing p", func() bool { return len(b.e.Status().Entries) == 0 })
	if len(a.e.Status().Entries) != 0 {
		t.Fatal("a still syncs p")
	}
	if a.read("p/x") != "x" || b.read("p/x") != "x" {
		t.Fatal("deleting a group must leave the files where they are")
	}
	b.write("p/x", "changed on b")
	time.Sleep(100 * time.Millisecond)
	if a.read("p/x") != "x" {
		t.Fatal("p still syncs after its group was deleted")
	}
	if _, err := a.e.CreateGroup("pair", nil); err != nil {
		t.Fatalf("the name is free again: %v", err)
	}
}

func TestD19_AnEntryBelongsToOneGroup(t *testing.T) {
	a := newMachine(t, "a")
	a.write("notes/x", "x")
	a.write("other/y", "y")
	if err := a.e.Add(a.path("notes"), nil, ""); err != nil { // "" is main
		t.Fatal(err)
	}
	if e := a.e.Entries()[0]; e.Group != proto.MainGroup {
		t.Fatalf("default group: %+v", e)
	}
	if err := a.e.Add(a.path("other"), nil, "no-such-group"); err == nil || !strings.Contains(err.Error(), "no-such-group") {
		t.Fatalf("unknown group: %v", err)
	}
	id, _ := a.e.CreateGroup("pair", nil) // a is not a member
	if err := a.e.Add(a.path("other"), nil, id); err == nil || !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("add from a non-member: %v", err)
	}
	a.e.GroupMembers(id, []string{"id-a"}, nil)
	if err := a.e.Add(a.path("notes"), nil, id); err == nil || !strings.Contains(err.Error(), "already synced in main") {
		t.Fatalf("same path twice: %v", err)
	}
	if err := a.e.Add(a.path("other"), nil, id); err != nil {
		t.Fatal(err)
	}
	if g := a.e.Groups(); g[0].ID != proto.MainGroup || g[0].Entries != 1 || g[1].Entries != 1 {
		t.Fatalf("groups %+v", g)
	}
}
