package engine

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/proto"
)

// three machines, fully linked, all known to each other, with a group
// "pair" = {a, b} made on a.
func pairFleet(t *testing.T) (a, b, c *machine, pair string) {
	t.Helper()
	a, b, c = newMachine(t, "a"), newMachine(t, "b"), newMachine(t, "c")
	link(t, a, b)
	link(t, b, c)
	link(t, a, c)
	for _, m := range []*machine{a, b, c} {
		eventually(t, m.name+" knows everyone", func() bool { return len(m.e.Machines()) == 3 })
	}
	id, err := a.e.CreateGroup("pair", []string{"id-a", "id-b"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "c knows pair", func() bool { _, ok := c.group(id); return ok })
	return a, b, c, id
}

func hasEntry(m *machine, path string) bool {
	for _, r := range m.e.Entries() {
		if r.Path == path {
			return true
		}
	}
	return false
}

func TestD19_ANonMemberNeverLearnsAScopedEntry(t *testing.T) {
	a, b, c, pair := pairFleet(t)
	a.write("secret/x.md", "x")
	if err := a.e.Add(a.path("secret"), nil, pair); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b has it", hasFile(b, "secret/x.md", "x"))
	time.Sleep(150 * time.Millisecond) // time enough for a leak
	if hasEntry(c, "secret") || c.read("secret/x.md") != "<missing>" {
		t.Fatalf("c learned the scoped entry: entries %v, file %q", c.e.Entries(), c.read("secret/x.md"))
	}
	for _, ev := range c.e.Events(0) {
		if ev.Path != "" && (ev.Path == "secret" || ev.Path[:min(7, len(ev.Path))] == "secret/") {
			t.Fatalf("c's activity mentions the scoped path: %+v", ev)
		}
	}
	// The group-wide entry still reaches everyone.
	a.write("shared/y.md", "y")
	a.add("shared")
	eventually(t, "c has the main entry", hasFile(c, "shared/y.md", "y"))
}

func TestD19_AScopedChangeDoesNotRelayThroughANonMember(t *testing.T) {
	a, b, c := newMachine(t, "a"), newMachine(t, "b"), newMachine(t, "c")
	link(t, b, c)
	link(t, a, c) // a-b only through c
	cutAB := link(t, a, b)
	for _, m := range []*machine{a, b, c} {
		eventually(t, m.name+" knows everyone", func() bool { return len(m.e.Machines()) == 3 })
	}
	pair, _ := a.e.CreateGroup("pair", []string{"id-a", "id-b"})
	a.write("p/x.md", "x")
	if err := a.e.Add(a.path("p"), nil, pair); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b has it over the direct link", hasFile(b, "p/x.md", "x"))
	cutAB()
	eventually(t, "a and b see the link down", func() bool { return len(a.e.Status().Peers) == 1 && len(b.e.Status().Peers) == 1 })
	a.write("p/x.md", "x2")
	a.e.Rescan()
	time.Sleep(300 * time.Millisecond)
	if b.read("p/x.md") != "x" || c.read("p/x.md") != "<missing>" {
		t.Fatalf("relayed through c: b=%q c=%q", b.read("p/x.md"), c.read("p/x.md"))
	}
	link(t, a, b)
	eventually(t, "b catches up on the direct link", hasFile(b, "p/x.md", "x2"))
}

func TestD19_AMachineAddedToAGroupLaterGetsItsEntries(t *testing.T) {
	a, _, c, pair := pairFleet(t)
	a.write("p/x.md", "x")
	if err := a.e.Add(a.path("p"), nil, pair); err != nil {
		t.Fatal(err)
	}
	c.write("p/x.md", "mine") // c's own copy: backed up on first add (D7, D13)
	c.write("p/extra.md", "extra")
	if err := a.e.GroupMembers(pair, []string{"id-c"}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "c got a's version", hasFile(c, "p/x.md", "x"))
	eventually(t, "c's own copy was backed up", func() bool {
		found := false
		filepathWalk(c.data+"/backups", func(content string) { found = found || content == "mine" })
		return found
	})
	eventually(t, "c's extra spread to a", hasFile(a, "p/extra.md", "extra"))
}

func filepathWalk(dir string, f func(content string)) {
	entries, _ := os.ReadDir(dir)
	for _, en := range entries {
		p := dir + "/" + en.Name()
		if en.IsDir() {
			filepathWalk(p, f)
		} else if b, err := os.ReadFile(p); err == nil {
			f(string(b))
		}
	}
}

func TestD19_EntryRecordsFromANonMemberPeerAreDropped(t *testing.T) {
	// Defence in depth: a peer with a stale view may send an entry of a
	// group this machine is not in; it must not be stored.
	a, b := newMachine(t, "a"), newMachine(t, "b")
	defer link(t, a, b)()
	eventually(t, "a knows b", func() bool { _, ok := a.machine("id-b"); return ok })
	solo, _ := a.e.CreateGroup("solo", []string{"id-a"})
	eventually(t, "b knows solo", func() bool { _, ok := b.group(solo); return ok })
	b.e.do(func() {
		b.e.applyEntry(proto.EntryRecord{Path: "leak", Group: solo, Clock: 99, By: "id-a"}, b.e.peers["id-a"])
	})
	if hasEntry(b, "leak") {
		t.Fatal("stored an entry of a group b is not in")
	}
}

func entryStatus(m *machine, path string) (EntryStatus, bool) {
	for _, es := range m.e.Status().Entries {
		if es.Path == path {
			return es, true
		}
	}
	return EntryStatus{}, false
}

func TestD19_LeavingAGroupFreezesItsEntriesAndReenteringResolvesEdits(t *testing.T) {
	a, b, _, pair := pairFleet(t)
	a.write("p/x.md", "one\n")
	if err := a.e.Add(a.path("p"), nil, pair); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b has it", hasFile(b, "p/x.md", "one\n"))
	if err := a.e.GroupMembers(pair, nil, []string{"id-b"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b's entry is frozen", func() bool { es, ok := entryStatus(b, "p"); return ok && es.Frozen && es.Group == pair })
	a.write("p/x.md", "two\n")
	a.e.Rescan()
	time.Sleep(200 * time.Millisecond)
	if b.read("p/x.md") != "one\n" {
		t.Fatalf("b still syncs p: %q", b.read("p/x.md"))
	}
	if es, _ := entryStatus(a, "p"); es.Frozen {
		t.Fatal("a's entry must stay live")
	}
	// An edit on b while out of scope is kept when b rejoins (SPEC §9, §7).
	b.write("p/x.md", "mine\n")
	b.e.Rescan()
	time.Sleep(100 * time.Millisecond)
	if err := a.e.GroupMembers(pair, []string{"id-b"}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b is live again", func() bool { es, ok := entryStatus(b, "p"); return ok && !es.Frozen })
	eventually(t, "both converge", func() bool { return a.read("p/x.md") == b.read("p/x.md") })
	eventually(t, "nothing lost: both edits exist on both", func() bool {
		for _, m := range []*machine{a, b} {
			have := map[string]bool{}
			filepathWalk(m.path("p"), func(c string) { have[c] = true })
			if !have["two\n"] || !have["mine\n"] {
				return false
			}
		}
		return true
	})
}

func TestD19_MoveEntryBetweenGroups(t *testing.T) {
	a, b, c, pair := pairFleet(t)
	a.write("p/x.md", "x")
	a.add("p") // main: everyone
	eventually(t, "c has it", hasFile(c, "p/x.md", "x"))
	if err := c.e.MoveEntry(c.path("p"), pair); err == nil || !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("a non-member of the target moved it: %v", err)
	}
	if err := a.e.MoveEntry(a.path("p"), "nope"); err == nil {
		t.Fatal("unknown group")
	}
	if err := a.e.MoveEntry(a.path("p"), pair); err != nil {
		t.Fatal(err)
	}
	eventually(t, "c's copy is frozen", func() bool { es, ok := entryStatus(c, "p"); return ok && es.Frozen })
	eventually(t, "b's record moved", func() bool { es, ok := entryStatus(b, "p"); return ok && es.Group == pair && !es.Frozen })
	a.write("p/x.md", "x2")
	a.e.Rescan()
	eventually(t, "b follows", hasFile(b, "p/x.md", "x2"))
	time.Sleep(150 * time.Millisecond)
	if c.read("p/x.md") != "x" {
		t.Fatalf("c followed a pair entry: %q", c.read("p/x.md"))
	}
	if err := b.e.MoveEntry(b.path("p"), proto.MainGroup); err != nil {
		t.Fatal(err)
	}
	eventually(t, "c is live again and catches up", func() bool {
		es, ok := entryStatus(c, "p")
		return ok && !es.Frozen && c.read("p/x.md") == "x2"
	})
	if err := a.e.MoveEntry(a.path("p"), proto.MainGroup); err == nil || !strings.Contains(err.Error(), "already in main") {
		t.Fatalf("move to the same group: %v", err)
	}
}

func TestD19_FrozenEntriesSurviveARestartAndStayFrozen(t *testing.T) {
	a, b, _, pair := pairFleet(t)
	a.write("p/x.md", "x")
	if err := a.e.Add(a.path("p"), nil, pair); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b has it", hasFile(b, "p/x.md", "x"))
	a.e.GroupMembers(pair, nil, []string{"id-b"})
	eventually(t, "frozen", func() bool { es, ok := entryStatus(b, "p"); return ok && es.Frozen })
	b.restart()
	es, ok := entryStatus(b, "p")
	if !ok || !es.Frozen {
		t.Fatalf("after restart: %+v %v", es, ok)
	}
	if b.e.Remove(b.path("p")) == nil {
		t.Fatal("a frozen entry cannot be removed from a machine out of its group")
	}
}
