package problems

import (
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/engine"
)

func TestD23_LinkProblems(t *testing.T) {
	links := []engine.LinkStatus{
		{Path: "s/ok", Target: "~/r/ok", State: engine.LinkSynced},
		{Path: "s/out", Target: "/etc", State: engine.LinkSkipped, Reason: "target outside your home folder", Since: t0},
		{Path: "s/new", Target: "~/r/new", State: engine.LinkPending, Since: t0.Add(-10 * time.Second)},
		{Path: "s/old", Target: "~/r/old", State: engine.LinkPending, Since: t0.Add(-2 * time.Minute)},
		{Path: "s/dir", Target: "~/r/dir", State: engine.LinkBlocked, Reason: "a folder is in the way here"},
		{Path: "s/gone", Target: "~/r/gone", State: engine.LinkDangling, Reason: "its target is not here"},
	}
	st := Input{Limits: lim(), Entries: []engine.EntryStatus{{Path: "s", Dir: true, LinksOn: true, Links: links,
		Skipped: []string{"s/out", "s/big.bin"}}}}
	ps := Derive(st, t0)
	want := "link-blocked:s/dir link-dangling:s/gone link-pending:s/old link-skipped:s/out skipped:s"
	if got := keys(ps); got != want {
		t.Fatalf("keys = %q, want %q", got, want)
	}
	if ps[0].Severity != Error {
		t.Fatal("a blocked link is an error")
	}
	for _, p := range ps {
		if p.Key == "skipped:s" && p.Message != "s: 1 file skipped (symlinks, or over 10 MB)" {
			t.Fatalf("the generic skipped count includes reported links: %q", p.Message)
		}
	}
}

// A replaced folder: its files waiting to be restored are a warning while
// the entry is not missing, and the replacement is one for an hour.
func TestD15_RestoringAndRootReplacedProblems(t *testing.T) {
	st := Input{Limits: lim(), Entries: []engine.EntryStatus{{Path: "notes", Dir: true, Restoring: []string{"notes/a.md", "notes/b.md"}}},
		Events: []engine.Event{{Kind: engine.EvRootReplaced, Path: "notes", Time: t0}}}
	ps := Derive(st, t0.Add(time.Minute))
	if got := keys(ps); got != "restoring:notes" { // the replacement itself is activity only
		t.Fatalf("keys = %q", got)
	}
	if ps[0].Severity != Warning || ps[0].Message != "notes: 2 files not here yet, being restored from the other machines" {
		t.Fatalf("%+v", ps[0])
	}
	st.Entries[0].Missing = true // missing locally says it already
	if got := keys(Derive(st, t0.Add(2*time.Hour))); got != "missing:notes" {
		t.Fatalf("keys = %q", got)
	}
}

// Hand test S1: a conflict problem goes when its copy is deleted, and
// the same condition is listed once.
func TestD20_ConflictProblemClearsWithItsCopyAndNoDuplicates(t *testing.T) {
	copy := "notes/todo.conflict-mac-20261026-133000-1a2b3c.md"
	ev := []engine.Event{
		{Seq: 1, Time: t0, Kind: engine.EvConflict, Path: copy, Detail: "notes/todo.md"},
		{Seq: 2, Time: t0, Kind: engine.EvConflict, Path: copy, Detail: "notes/todo.md"},
	}
	in := Input{Limits: lim(), Events: ev, ConflictsKnown: true, Conflicts: []string{copy}}
	if got := keys(Derive(in, t0)); got != "conflict:"+copy {
		t.Fatalf("keys = %q", got)
	}
	in.Conflicts = nil // the copy was deleted
	if ps := Derive(in, t0); len(ps) != 0 {
		t.Fatalf("stale: %+v", ps)
	}
}

// F2 and review #3: an unwatched folder is an error until it is watched
// again; an unreliable identity is a standing warning.
func TestD15_UnwatchedAndUnreliableProblems(t *testing.T) {
	st := Input{Limits: lim(), Entries: []engine.EntryStatus{{Path: "notes", Dir: true, Unwatched: true, IdentityUnreliable: true}}}
	ps := Derive(st, t0)
	if got := keys(ps); got != "unwatched:notes identity:notes" || ps[0].Severity != Error {
		t.Fatalf("%q %+v", got, ps)
	}
	st.Entries[0].Unwatched, st.Entries[0].IdentityUnreliable = false, false
	if ps := Derive(st, t0); len(ps) != 0 {
		t.Fatalf("%+v", ps)
	}
}

// Review 5: subfolders whose watch could not be added (the watch limit)
// are an error too, naming them, until a retry watches them.
func TestD15_PartlyWatchedFolderIsAnError(t *testing.T) {
	st := Input{Limits: lim(), Entries: []engine.EntryStatus{{Path: "notes", Dir: true, UnwatchedFolders: []string{"notes/a", "notes/b"}}}}
	ps := Derive(st, t0)
	if len(ps) != 1 || ps[0].Key != "unwatched:notes" || ps[0].Severity != Error || !strings.Contains(ps[0].Message, "notes/a") {
		t.Fatalf("%+v", ps)
	}
}

func TestD23_LostLinkIsAWarningForAnHour(t *testing.T) {
	ev := engine.Event{Kind: engine.EvLinkLost, Path: "s/l", Detail: "../r/a", Time: t0}
	if ps := Derive(Input{Events: []engine.Event{ev}}, t0.Add(30*time.Minute)); len(ps) != 1 || ps[0].Severity != Warning {
		t.Fatalf("got %+v", ps)
	}
	if ps := Derive(Input{Events: []engine.Event{ev}}, t0.Add(2*time.Hour)); len(ps) != 0 {
		t.Fatalf("still there after an hour: %+v", ps)
	}
}
