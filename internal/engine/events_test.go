package engine

import (
	"os"
	"strings"
	"testing"
	"time"
)

func (m *machine) hasEvent(kind, path, peer string) func() bool {
	return func() bool {
		for _, ev := range m.e.Events(0) {
			if ev.Kind == kind && (path == "" || ev.Path == path) && (peer == "" || ev.Peer == peer) {
				return true
			}
		}
		return false
	}
}

func TestEventsTellTheStory(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("n/f.md", "v1")
	a.add("n")
	eventually(t, "entry added on a", a.hasEvent(EvEntryAdded, "n", ""))
	cut := link(t, a, b)
	eventually(t, "link up on b", b.hasEvent(EvLinkUp, "", "a"))
	eventually(t, "b received f.md from a", b.hasEvent(EvReceived, "n/f.md", "a"))

	b.write("n/f.md", "v2 on b")
	eventually(t, "change made on b", b.hasEvent(EvChanged, "n/f.md", ""))
	eventually(t, "a received it", a.hasEvent(EvReceived, "n/f.md", "b"))

	os.Remove(b.path("n/f.md"))
	eventually(t, "a deleted it", a.hasEvent(EvDeleted, "n/f.md", "b"))

	cut()
	eventually(t, "link down on a", a.hasEvent(EvLinkDown, "", "b"))
}

func TestEventsForMergesAndConflicts(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	concurrentEdits(t, a, b, "c/f.md", fiveLines, "1 from a\n2\n3\n4\n5\n", "1\n2\n3\n4\n5 from b\n")
	link(t, a, b)
	eventually(t, "merge event", func() bool { return a.hasEvent(EvMerged, "c/f.md", "")() || b.hasEvent(EvMerged, "c/f.md", "")() })

	c, d := newMachine(t, "c"), newMachine(t, "d")
	concurrentEdits(t, c, d, "x/g.md", "base\n", "on c\n", "on d\n")
	link(t, c, d)
	eventually(t, "conflict event", func() bool {
		for _, ev := range c.e.Events(0) {
			if ev.Kind == EvConflict && strings.HasPrefix(ev.Path, "x/g.conflict-") && ev.Detail == "x/g.md" {
				return true
			}
		}
		return false
	})
}

func TestEventsAfterAndRing(t *testing.T) {
	a := newMachine(t, "a")
	for i := 0; i < maxEvents+20; i++ {
		a.e.do(func() { a.e.event(EvError, "p", "", "x") })
	}
	all := a.e.Events(0)
	if len(all) != maxEvents {
		t.Fatalf("ring holds %d", len(all))
	}
	last := all[len(all)-1].Seq
	if got := a.e.Events(last - 3); len(got) != 3 || got[0].Seq != last-2 {
		t.Fatalf("after: %+v", got)
	}
	if got := a.e.Events(last); len(got) != 0 {
		t.Fatalf("nothing new: %+v", got)
	}
	if all[0].Time.IsZero() || time.Since(all[0].Time) > time.Minute {
		t.Fatal("events carry their time")
	}
}
