package engine

import (
	"os"
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
