package engine

import (
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/proto"
)

// newcomerFleet: a and b sync ~/notes; b goes offline and edits it; a adds
// c while b is away (c joins with every member's record, as `enigma join`
// hands them over) and c catches up from a; a goes offline. b has never
// heard of c when c dials it.
func newcomerFleet(t *testing.T, offline func(b *machine)) (a, b, c *machine) {
	t.Helper()
	a, b = newMachine(t, "a"), newMachine(t, "b")
	a.write("notes/x.md", "v1")
	a.add("notes")
	cutAB := link(t, a, b)
	eventually(t, "b has v1", hasFile(b, "notes/x.md", "v1"))
	cutAB()
	eventually(t, "a-b down", func() bool { return len(a.e.Status().Peers) == 0 && len(b.e.Status().Peers) == 0 })

	b.write("notes/x.md", "v2 from b, offline")
	b.e.Rescan()
	if offline != nil {
		offline(b)
	}

	boot := append(a.e.Machines(), proto.MachineRecord{ID: "id-c", Name: "c", Clock: 1, By: "id-c"})
	c = newMachine(t, "c", func(o *Options) { o.Bootstrap = boot })
	cutAC := link(t, a, c)
	eventually(t, "c has v1 from a", hasFile(c, "notes/x.md", "v1"))
	cutAC()
	eventually(t, "a-c down", func() bool { return len(a.e.Status().Peers) == 0 && len(c.e.Status().Peers) == 0 })
	return a, b, c
}

// §6.5 catch-up with a member that does not know us yet, in the usual
// flow: the newcomer's own main record (made at its first start) is newer
// than b's, and taking it makes b send its scope again.
func TestCatchUpWithAMemberThatDoesNotKnowUsYet(t *testing.T) {
	_, b, c := newcomerFleet(t, nil)
	link(t, c, b) // c dials b; b learns c's record only from this link
	eventually(t, "c gets b's offline edit", hasFile(c, "notes/x.md", "v2 from b, offline"))
}

// The same, when b changed a group while offline so that every group
// record it holds is at least as new as c's: nothing on the link makes b
// send its scope again once it learns c.
func TestCatchUpWithAMemberThatDoesNotKnowUsYetAfterOfflineGroupChange(t *testing.T) {
	_, b, c := newcomerFleet(t, func(b *machine) {
		// b raises its clock well past what c's first main record will
		// carry, then touches main: its main is the newest anywhere.
		for i := 0; i < 20; i++ {
			if _, err := b.e.CreateGroup("g"+string(rune('a'+i)), []string{"id-b"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := b.e.GroupMembers(proto.MainGroup, nil, []string{"id-a"}); err != nil {
			t.Fatal(err)
		}
		if err := b.e.GroupMembers(proto.MainGroup, []string{"id-a"}, nil); err != nil {
			t.Fatal(err)
		}
	})
	link(t, c, b)
	time.Sleep(200 * time.Millisecond)
	eventually(t, "c gets b's offline edit", hasFile(c, "notes/x.md", "v2 from b, offline"))
}
