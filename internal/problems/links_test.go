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

// Owner decision: what a catch-up scan found that was not seen live is a
// warning per entry; deletes that went out point at the peers' trash.
func TestWhileAwayIsAWarningWithATrashHint(t *testing.T) {
	s := &engine.AwaySummary{Kind: engine.AwayNotRunning, At: t0, Deleted: 3, Replaced: true, Restored: 12, Strike: 1}
	ps := Derive(Input{Entries: []engine.EntryStatus{{Path: "notes", Dir: true, WhileAway: s}}}, t0)
	if len(ps) != 1 || ps[0].Key != "while-away:notes" || ps[0].Severity != Warning {
		t.Fatalf("%+v", ps)
	}
	for _, want := range []string{"while Enigma was not running", "3 files you deleted here were deleted on the other machines",
		"~/notes was replaced", "12 files not here were restored", "strike 1/3"} {
		if !strings.Contains(ps[0].Message, want) {
			t.Errorf("message %q lacks %q", ps[0].Message, want)
		}
	}
	// Hand test 4, W3: like every problem, the message starts with the
	// path; behind the warning mark, "~ ~/notes:" read as two marks.
	if !strings.HasPrefix(ps[0].Message, "notes: while Enigma was not running: ") {
		t.Errorf("message %q does not start with the entry's path", ps[0].Message)
	}
	if !strings.Contains(ps[0].Hint, "enigma trash list") || !strings.Contains(ps[0].Hint, "enigma trash restore") {
		t.Fatalf("hint %q", ps[0].Hint)
	}
	s.Deleted = 0
	if ps := Derive(Input{Entries: []engine.EntryStatus{{Path: "notes", Dir: true, WhileAway: s}}}, t0); strings.Contains(ps[0].Hint, "trash") {
		t.Fatalf("a trash hint without deletes: %q", ps[0].Hint)
	}
}

// Hand test 3, F2: a write that keeps failing is an error naming the path
// while it fails, and nothing once it is written.
func TestWriteFailingIsAnErrorWhileItFails(t *testing.T) {
	in := Input{Entries: []engine.EntryStatus{{Path: "notes", Dir: true, WriteFailing: []string{"notes/perm/p.md"}}}}
	ps := Derive(in, t0)
	if len(ps) != 1 || ps[0].Key != "write-failing:notes/perm/p.md" || ps[0].Severity != Error {
		t.Fatalf("%+v", ps)
	}
	in.Entries[0].WriteFailing = nil
	if ps := Derive(in, t0); len(ps) != 0 {
		t.Fatalf("%+v", ps)
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

// Hand test 2, item 4 (S1 rules): a lost link is handled when it is in
// the trash; nothing is wrong now, so it is activity only, not a standing
// problem that lingers.
func TestD23_LostLinkIsActivityOnly(t *testing.T) {
	ev := engine.Event{Kind: engine.EvLinkLost, Path: "s/l", Detail: "../r/a", Time: t0}
	if ps := Derive(Input{Events: []engine.Event{ev}}, t0.Add(time.Minute)); len(ps) != 0 {
		t.Fatalf("got %+v", ps)
	}
}
