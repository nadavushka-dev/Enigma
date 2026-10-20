package engine

import (
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/version"
)

type V = version.Vector

func ver(hash string, vec V, by string, mtime int64) version.Version {
	return version.Version{Hash: hash, Vector: vec, By: by, ByName: by + "-name", Mtime: mtime}
}

func tomb(vec V, by string) version.Version {
	return version.Version{Hash: version.Deleted, Vector: vec, By: by, ByName: by + "-name"}
}

func ptr(v version.Version) *version.Version { return &v }

func mustAction(t *testing.T, d Decision, want Action) {
	t.Helper()
	if d.Action != want {
		t.Fatalf("action = %v, want %v (decision %+v)", d.Action, want, d)
	}
}

func TestDecide_OlderOrEqualIsIgnored(t *testing.T) {
	cur := ver("h2", V{"a": 2}, "a", 1)
	mustAction(t, Decide("p", &cur, ver("h1", V{"a": 1}, "a", 1), false), Ignore)
	mustAction(t, Decide("p", &cur, cur, true), Ignore)
}

func TestDecide_NewerNeedsFetchThenApplies(t *testing.T) {
	cur := ver("h1", V{"a": 1}, "a", 1)
	in := ver("h2", V{"a": 1, "b": 1}, "b", 2)
	mustAction(t, Decide("p", &cur, in, false), Fetch)
	d := Decide("p", &cur, in, true)
	mustAction(t, d, Apply)
	if d.Result.Hash != "h2" || version.Compare(d.Result.Vector, in.Vector) != version.Equal {
		t.Fatalf("result = %+v", d.Result)
	}
}

func TestDecide_NewerWithSameContentIsRecordedWithoutFetch(t *testing.T) {
	cur := ver("h1", V{"a": 1}, "a", 1)
	in := ver("h1", V{"a": 1, "b": 1}, "b", 2)
	d := Decide("p", &cur, in, false)
	mustAction(t, d, Record)
	if version.Compare(d.Result.Vector, in.Vector) != version.Equal {
		t.Fatalf("vector = %v", d.Result.Vector)
	}
}

func TestDecide_UntrackedFetches(t *testing.T) {
	mustAction(t, Decide("p", nil, ver("h", V{"a": 1}, "a", 1), false), Fetch)
	mustAction(t, Decide("p", nil, ver("h", V{"a": 1}, "a", 1), true), Apply)
}

func TestDecide_UntrackedTombstoneIsJustRecorded(t *testing.T) {
	mustAction(t, Decide("p", nil, tomb(V{"a": 2}, "a"), false), Record)
}

func TestD8_NewerDeleteDeletesWithoutFetch(t *testing.T) {
	cur := ver("h1", V{"a": 1}, "a", 1)
	d := Decide("p", &cur, tomb(V{"a": 1, "b": 1}, "b"), false)
	mustAction(t, d, Delete)
	if !d.Result.IsDeleted() {
		t.Fatal("result must be the tombstone")
	}
}

func TestD8_EditBeatsConcurrentDelete(t *testing.T) {
	edit := ver("h2", V{"a": 2}, "a", 5)
	del := tomb(V{"a": 1, "b": 1}, "b")

	// Local edit, incoming delete: keep the edit, no fetch, and announce it.
	d := Decide("p", &edit, del, false)
	mustAction(t, d, Record)
	if d.Result.Hash != "h2" || version.Compare(d.Result.Vector, del.Vector) != version.After {
		t.Fatalf("edit must win and dominate the delete: %+v", d.Result)
	}

	// Local delete, incoming edit: restore the edit.
	mustAction(t, Decide("p", &del, edit, false), Fetch)
	d2 := Decide("p", &del, edit, true)
	mustAction(t, d2, Apply)

	// Both sides end with the identical version.
	if d.Result.Hash != d2.Result.Hash || version.Compare(d.Result.Vector, d2.Result.Vector) != version.Equal {
		t.Fatalf("sides disagree: %+v vs %+v", d.Result, d2.Result)
	}
}

func TestDecide_ConcurrentSameContentMergesVectors(t *testing.T) {
	a := ver("same", V{"a": 1}, "a", 1)
	b := ver("same", V{"b": 1}, "b", 2)
	d := Decide("p", &a, b, false)
	mustAction(t, d, Record)
	if version.Compare(d.Result.Vector, V{"a": 1, "b": 1}) != version.Equal {
		t.Fatalf("vector = %v", d.Result.Vector)
	}
	if d.Copy != nil || d.Backup {
		t.Fatal("identical content is not a conflict")
	}
}

func TestDecide_ConcurrentDeletesMerge(t *testing.T) {
	a, b := tomb(V{"a": 2}, "a"), tomb(V{"b": 2}, "b")
	mustAction(t, Decide("p", &a, b, false), Record)
}

func TestD4_ConflictNewerWinsLoserBecomesCopy(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 15, 30, 12, 0, time.UTC).UnixNano()
	older := ver("old", V{"a": 1, "x": 1}, "a", t0)
	newer := ver("new", V{"b": 1, "x": 1}, "b", t0+int64(time.Second))

	// Local is older: fetch, then apply incoming and keep local as a copy.
	mustAction(t, Decide(".claude/CLAUDE.md", &older, newer, false), Fetch)
	d := Decide(".claude/CLAUDE.md", &older, newer, true)
	mustAction(t, d, Apply)
	if d.Copy == nil || d.Copy.FromIncoming {
		t.Fatalf("local (older) content must be copied: %+v", d.Copy)
	}
	if d.Copy.Path != ".claude/CLAUDE.conflict-a-name-20261020-153012-old.md" {
		t.Fatalf("copy path = %s", d.Copy.Path)
	}

	// Mirror side: local is newer; it keeps its content and copies the incoming.
	d2 := Decide(".claude/CLAUDE.md", &newer, older, true)
	mustAction(t, d2, Record)
	if d2.Copy == nil || !d2.Copy.FromIncoming {
		t.Fatalf("incoming (older) content must be copied: %+v", d2.Copy)
	}

	// Determinism (SPEC §7): both sides produce the same result and copy.
	if d.Result.Hash != "new" || d2.Result.Hash != "new" ||
		version.Compare(d.Result.Vector, d2.Result.Vector) != version.Equal {
		t.Fatalf("results differ: %+v vs %+v", d.Result, d2.Result)
	}
	if d.Copy.Path != d2.Copy.Path || d.Copy.Version.Hash != d2.Copy.Version.Hash ||
		version.Compare(d.Copy.Version.Vector, d2.Copy.Version.Vector) != version.Equal {
		t.Fatalf("copies differ: %+v vs %+v", d.Copy, d2.Copy)
	}
	if d.Copy.Version.Hash != "old" {
		t.Fatalf("copy must hold the loser's content: %+v", d.Copy.Version)
	}
}

func TestD4_ConflictTieBreaksOnMachineID(t *testing.T) {
	a := ver("ha", V{"a": 1}, "a", 100)
	b := ver("hb", V{"b": 1}, "b", 100)
	d1 := Decide("f", &a, b, true)
	d2 := Decide("f", &b, a, true)
	if d1.Result.Hash != "hb" || d2.Result.Hash != "hb" {
		t.Fatalf("higher machine ID must win ties: %s / %s", d1.Result.Hash, d2.Result.Hash)
	}
}

func TestD7_SeedBeatsPreexistingWithBackup(t *testing.T) {
	seed := ver("adder", V{"x": 1}, "x", 1)
	seed.Origin = version.Seed
	pre := ver("mine", V{"b": 1}, "b", 999) // newer mtime must not matter
	pre.Origin = version.Preexisting

	// On the machine with the preexisting copy: fetch, back up, apply.
	mustAction(t, Decide("f", &pre, seed, false), Fetch)
	d := Decide("f", &pre, seed, true)
	mustAction(t, d, Apply)
	if !d.Backup || d.Copy != nil {
		t.Fatalf("want local backup and no conflict copy: %+v", d)
	}
	// On the adding machine: keep, no fetch needed, no backup there.
	d2 := Decide("f", &seed, pre, false)
	mustAction(t, d2, Record)
	if d2.Backup || d2.Copy != nil || d2.Result.Hash != "adder" {
		t.Fatalf("adder side: %+v", d2)
	}
	if version.Compare(d.Result.Vector, d2.Result.Vector) != version.Equal {
		t.Fatal("sides disagree on the resolved vector")
	}
}

// Regression: the adder resolves seed-vs-preexisting without seeing the
// preexisting content and announces the resolved (dominating) version. The
// machine holding the preexisting copy must still back it up.
func TestD7_ResolvedSeedOverPreexistingStillBacksUp(t *testing.T) {
	seed := ver("adder", V{"x": 1}, "x", 1)
	seed.Origin = version.Seed
	pre := ver("mine", V{"b": 1}, "b", 2)
	pre.Origin = version.Preexisting
	resolved := Decide("f", &seed, pre, false).Result // what the adder announces

	d := Decide("f", &pre, resolved, true)
	mustAction(t, d, Apply)
	if !d.Backup {
		t.Fatal("preexisting content replaced without a backup")
	}
	// Same content needs no backup.
	same := pre
	same.Vector = V{"b": 1, "x": 1}
	if Decide("f", &pre, same, true).Backup {
		t.Fatal("identical content must not be backed up")
	}
}

func TestD7_IdenticalPreexistingResolvesToSeed(t *testing.T) {
	seed := ver("same", V{"a": 1}, "a", 1)
	seed.Origin = version.Seed
	pre := ver("same", V{"z": 1}, "z", 1) // higher machine ID than the adder
	pre.Origin = version.Preexisting
	for _, d := range []Decision{Decide("f", &seed, pre, false), Decide("f", &pre, seed, false)} {
		if d.Result.Origin == version.Preexisting {
			t.Fatal("agreed content must not stay marked preexisting (would cause needless backups)")
		}
	}
}

func TestD7_EditedPreexistingIsANormalConflict(t *testing.T) {
	seed := ver("adder", V{"x": 1}, "x", 1)
	seed.Origin = version.Seed
	edited := ver("mine2", V{"b": 2}, "b", 2) // a real edit after adoption
	d := Decide("f", &edited, seed, true)
	if d.Backup {
		t.Fatal("a real edit must not be silently backed up")
	}
	if d.Copy == nil {
		t.Fatal("want a conflict copy")
	}
}

func TestConflictName(t *testing.T) {
	ts := time.Date(2026, 10, 20, 15, 30, 12, 0, time.FixedZone("x", 3*3600)).UnixNano()
	const h = "1a2b3c4d5e6f"
	cases := map[string]string{
		"CLAUDE.md":        "CLAUDE.conflict-dev_box-20261020-123012-1a2b3c.md",
		"dir/notes.txt":    "dir/notes.conflict-dev_box-20261020-123012-1a2b3c.txt",
		".zshrc":           ".zshrc.conflict-dev_box-20261020-123012-1a2b3c",
		"a/b/Makefile":     "a/b/Makefile.conflict-dev_box-20261020-123012-1a2b3c",
		"a/archive.tar.gz": "a/archive.tar.conflict-dev_box-20261020-123012-1a2b3c.gz",
	}
	for in, want := range cases {
		if got := ConflictName(in, "dev box", ts, h); got != want {
			t.Errorf("ConflictName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Regression (simulation seed 1): two conflicts lost by the same machine in
// the same second got the same copy name and the second copy was dropped.
func TestD4_CopiesFromSameMachineSameSecondDoNotCollide(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 8, 37, 59, 0, time.UTC).UnixNano()
	win := ver(fsx.Hash([]byte("winner")), V{"b": 1}, "b", t0+int64(500*time.Millisecond))
	l1 := ver(fsx.Hash([]byte("loser one")), V{"a": 1}, "a", t0)
	l2 := ver(fsx.Hash([]byte("loser two")), V{"a": 2}, "a", t0+int64(100*time.Millisecond))
	d1 := Decide("two.md", &l1, win, true)
	d2 := Decide("two.md", &l2, win, true)
	if d1.Copy == nil || d2.Copy == nil || d1.Copy.Path == d2.Copy.Path {
		t.Fatalf("copy paths collide: %+v / %+v", d1.Copy, d2.Copy)
	}
}

// ---- §7 step 3: merged concurrent edits --------------------------------------

func TestMerge_CleanMergeIsAppliedAsOneVersion(t *testing.T) {
	a := ver("ha", V{"a": 2, "b": 1}, "a", 100)
	b := ver("hb", V{"a": 1, "b": 2}, "b", 200)
	m := Merged{Hash: "hm", Content: []byte("merged")}
	d := DecideMerged("f", &a, b, m)
	mustAction(t, d, Apply)
	if d.Copy != nil {
		t.Fatal("a clean merge makes no conflict copy")
	}
	if string(d.Content) != "merged" || d.Result.Hash != "hm" {
		t.Fatalf("content %q result %+v", d.Content, d.Result)
	}
	if version.Compare(d.Result.Vector, V{"a": 2, "b": 2}) != version.Equal {
		t.Fatalf("vector %v: merged, with no extra counter", d.Result.Vector)
	}
	if d.Result.Mtime != 200 || d.Result.By != "b" || d.Result.Origin != version.Edit {
		t.Fatalf("result %+v: later mtime, author of the step-4 winner", d.Result)
	}
	if len(d.Result.Parents) != 2 || d.Result.Parents[0] != "ha" || d.Result.Parents[1] != "hb" {
		t.Fatalf("parents %v", d.Result.Parents)
	}
}

func TestMerge_BothMachinesMakeTheSameVersion(t *testing.T) {
	a := ver("ha", V{"a": 2, "b": 1}, "a", 100)
	b := ver("hb", V{"a": 1, "b": 2}, "b", 100)
	b.Exec = true
	m := Merged{Hash: "hm", Content: []byte("merged")}
	da, db := DecideMerged("f", &a, b, m), DecideMerged("f", &b, a, m)
	ra, rb := da.Result, db.Result
	if ra.Hash != rb.Hash || ra.Mtime != rb.Mtime || ra.By != rb.By || ra.Exec != rb.Exec ||
		version.Compare(ra.Vector, rb.Vector) != version.Equal {
		t.Fatalf("machines diverge:\n%+v\n%+v", ra, rb)
	}
}

func TestMerge_ResultEqualToLocalIsJustRecorded(t *testing.T) {
	a := ver("ha", V{"a": 2, "b": 1}, "a", 300)
	b := ver("hb", V{"a": 1, "b": 2}, "b", 100)
	d := DecideMerged("f", &a, b, Merged{Hash: "ha", Content: []byte("same as a")})
	mustAction(t, d, Record)
	if d.Result.Hash != "ha" || version.Compare(d.Result.Vector, V{"a": 2, "b": 2}) != version.Equal {
		t.Fatalf("result %+v", d.Result)
	}
}

func TestMerge_FirstAddAndDeleteRulesComeFirst(t *testing.T) {
	m := Merged{Hash: "hm", Content: []byte("merged")}
	seed := ver("hs", V{"a": 1}, "a", 1)
	seed.Origin = version.Seed
	pre := ver("hp", V{"b": 1}, "b", 2)
	pre.Origin = version.Preexisting
	if d := DecideMerged("f", &pre, seed, m); d.Result.Hash != "hs" || !d.Backup {
		t.Fatalf("D7: seed must win over a preexisting copy, got %+v", d)
	}
	edit := ver("he", V{"a": 2, "b": 1}, "a", 1)
	del := tomb(V{"a": 1, "b": 2}, "b")
	if d := DecideMerged("f", &edit, del, m); d.Result.Hash != "he" {
		t.Fatalf("D8: the edit must beat the delete, got %+v", d)
	}
}

// Regression: one machine merged and the other fell back (base missing), so
// the two versions had the same vector, mtime and author. With no tie-break
// left, each side kept its own and copied the other's forever.
func TestD4_FullTieBreaksOnHash(t *testing.T) {
	x := ver("hx", V{"a": 2, "b": 1}, "b", 100)
	y := ver("hy", V{"a": 2, "b": 1}, "b", 100)
	dx, dy := Decide("f", &x, y, true), Decide("f", &y, x, true)
	if dx.Result.Hash != dy.Result.Hash || dx.Result.Hash != "hy" {
		t.Fatalf("winners differ: %q vs %q", dx.Result.Hash, dy.Result.Hash)
	}
	if dx.Copy == nil || dy.Copy == nil || dx.Copy.Path != dy.Copy.Path {
		t.Fatalf("copies differ: %+v / %+v", dx.Copy, dy.Copy)
	}
}
