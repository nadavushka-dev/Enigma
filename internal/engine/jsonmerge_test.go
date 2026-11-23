package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/proto"
)

// Two keys on adjacent lines: a line merge sees touching edits.
const authBase = "{\n  \"a\": \"a0\",\n  \"b\": \"b0\"\n}"

func auth(a, b string) string {
	return "{\n  \"a\": \"" + a + "\",\n  \"b\": \"" + b + "\"\n}"
}

// jsonEdits syncs base in folder c with the given merge setting, then
// edits it on both machines while they are cut off, a's edit older.
func jsonEdits(t *testing.T, a, b *machine, rel, base, onA, onB string, mode proto.MergeMode, newestBy string) {
	t.Helper()
	a.write(rel, base)
	a.add("c")
	if mode != "" {
		if err := a.e.SetMerge(a.path("c"), mode, newestBy); err != nil {
			t.Fatal(err)
		}
	}
	cut := link(t, a, b)
	eventually(t, "synced", hasFile(b, rel, base))
	eventually(t, "setting on b", func() bool {
		r := entryRecord(b, "c")
		return r.Merge == mode && r.MergeNewestBy == newestBy
	})
	cut()
	a.writeAged(rel, onA, time.Hour)
	a.e.Rescan()
	b.write(rel, onB)
	b.e.Rescan()
	link(t, a, b)
}

func noCopies(t *testing.T, a, b *machine, stem string) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	if n := len(conflictCopies(a, "c", stem)) + len(conflictCopies(b, "c", stem)); n != 0 {
		t.Fatalf("%d conflict copies for a clean merge", n)
	}
}

func TestJSONMerge_LinesAloneMakeACopy(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	jsonEdits(t, a, b, "c/auth.json", authBase, auth("a1", "b0"), auth("a0", "b1"), "", "")
	eventually(t, "copy on a", func() bool { return len(conflictCopies(a, "c", "auth")) == 1 })
}

func TestJSONMerge_DifferentKeysMergeCleanly(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	jsonEdits(t, a, b, "c/auth.json", authBase, auth("a1", "b0"), auth("a0", "b1"), proto.MergeJSON, "")
	want := auth("a1", "b1")
	eventually(t, "merged on a", hasFile(a, "c/auth.json", want))
	eventually(t, "merged on b", hasFile(b, "c/auth.json", want))
	noCopies(t, a, b, "auth")
}

// The same key changed on both sides is a real conflict: the newer edit
// wins whole and the other is the copy. A line merge must not run instead.
func TestJSONMerge_SameKeyFallsBackToCopy(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	base := "{\n  \"a\": {\n    \"t\": \"a0\",\n    \"x\": 0\n  }\n}"
	onA := "{\n  \"a\": {\n    \"t\": \"a1\",\n    \"x\": 0\n  }\n}" // line merge would take t from a
	onB := "{\n  \"a\": {\n    \"t\": \"a0\",\n    \"x\": 2\n  }\n}" // and x from b
	jsonEdits(t, a, b, "c/auth.json", base, onA, onB, proto.MergeJSON, "")
	eventually(t, "newer wins whole on a", hasFile(a, "c/auth.json", onB))
	eventually(t, "copy on a", func() bool {
		for _, c := range conflictCopies(a, "c", "auth") {
			return c == onA
		}
		return false
	})
}

// The fleet's nightly case: both sides refresh the same account and others.
// With newest-by, the larger field wins that key whole, the rest merges,
// and there is no copy, whichever edit is newer on disk.
func TestJSONMerge_NewestByResolvesTheSameKey(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	base := `{"x": {"t": "x0", "exp": 1}, "y": {"t": "y0", "exp": 1}, "z": {"t": "z0", "exp": 1}}`
	onA := `{"x": {"t": "x1", "exp": 9}, "y": {"t": "y1", "exp": 5}, "z": {"t": "z0", "exp": 1}}`
	onB := `{"x": {"t": "x0", "exp": 1}, "y": {"t": "y2", "exp": 3}, "z": {"t": "z2", "exp": 4}}`
	jsonEdits(t, a, b, "c/auth.json", base, onA, onB, proto.MergeJSON, "exp")
	want := `{"x":{"t":"x1","exp":9},"y":{"t":"y1","exp":5},"z":{"t":"z2","exp":4}}`
	eventually(t, "merged on a", hasFile(a, "c/auth.json", want))
	eventually(t, "merged on b", hasFile(b, "c/auth.json", want))
	noCopies(t, a, b, "auth")
}

func TestJSONMerge_NonJSONFilesStillMergeLines(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	jsonEdits(t, a, b, "c/f.md", fiveLines, "1 from a\n2\n3\n4\n5\n", "1\n2\n3\n4\n5 from b\n", proto.MergeJSON, "")
	eventually(t, "merged on b", hasFile(b, "c/f.md", "1 from a\n2\n3\n4\n5 from b\n"))
}

func TestJSONMerge_SetMergeRefusals(t *testing.T) {
	a := newMachine(t, "a")
	if err := a.e.SetMerge(a.path("nope"), proto.MergeJSON, ""); err == nil || !strings.Contains(err.Error(), "not synced") {
		t.Fatalf("unsynced path: %v", err)
	}
	a.write("f.json", "{}")
	a.add("f.json")
	if err := a.e.SetMerge(a.path("f.json"), "yaml", ""); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
	if err := a.e.SetMerge(a.path("f.json"), proto.MergeLines, "exp"); err == nil {
		t.Fatal("newest-by without json was accepted")
	}
	if err := a.e.SetMerge(a.path("f.json"), proto.MergeJSON, "exp"); err != nil {
		t.Fatalf("a file entry: %v", err)
	}
	if got := a.e.Status().Entries[0]; got.Merge != proto.MergeJSON || got.MergeNewestBy != "exp" {
		t.Fatalf("status Merge = %q, newest by %q", got.Merge, got.MergeNewestBy)
	}
	if err := a.e.SetMerge(a.path("f.json"), proto.MergeLines, ""); err != nil {
		t.Fatal(err)
	}
	if got := a.e.Status().Entries[0]; got.Merge != "" || got.MergeNewestBy != "" {
		t.Fatalf("lines is the default and shows as empty, got %+v", got)
	}
}

func entryRecord(m *machine, path string) proto.EntryRecord {
	var got proto.EntryRecord
	m.e.do(func() { got = m.e.st.Entries[path] })
	return got
}
