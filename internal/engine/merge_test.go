package engine

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const fiveLines = "1\n2\n3\n4\n5\n"

// concurrentEdits syncs base to a and b, then edits both while cut off.
func concurrentEdits(t *testing.T, a, b *machine, rel, base, onA, onB string) {
	t.Helper()
	a.write(rel, base)
	a.add(filepath.Dir(rel))
	cut := link(t, a, b)
	eventually(t, "synced", hasFile(b, rel, base))
	cut()
	a.writeAged(rel, onA, time.Hour)
	a.e.Rescan()
	b.write(rel, onB)
	b.e.Rescan()
}

func TestS7_ConcurrentTextEditsMergeCleanly(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	concurrentEdits(t, a, b, "c/CLAUDE.md", fiveLines, "1 from a\n2\n3\n4\n5\n", "1\n2\n3\n4\n5 from b\n")
	link(t, a, b)
	want := "1 from a\n2\n3\n4\n5 from b\n"
	eventually(t, "merged on a", hasFile(a, "c/CLAUDE.md", want))
	eventually(t, "merged on b", hasFile(b, "c/CLAUDE.md", want))
	time.Sleep(200 * time.Millisecond)
	if n := len(conflictCopies(a, "c", "CLAUDE")) + len(conflictCopies(b, "c", "CLAUDE")); n != 0 {
		t.Fatalf("%d conflict copies for a clean merge", n)
	}
	if len(a.e.Status().Conflicts)+len(b.e.Status().Conflicts) != 0 {
		t.Fatal("a clean merge is not a conflict")
	}
}

func TestS7_MergedVersionKeepsSyncing(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	concurrentEdits(t, a, b, "c/f.md", fiveLines, "1 from a\n2\n3\n4\n5\n", "1\n2\n3\n4\n5 from b\n")
	link(t, a, b)
	eventually(t, "merged on b", hasFile(b, "c/f.md", "1 from a\n2\n3\n4\n5 from b\n"))
	a.write("c/f.md", "after the merge\n")
	eventually(t, "later edit reaches b", hasFile(b, "c/f.md", "after the merge\n"))
}

func TestS7_OverlappingTextEditsFallBackToCopy(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	concurrentEdits(t, a, b, "c/f.md", fiveLines, "1\n2\n3 from a\n4\n5\n", "1\n2\n3 from b\n4\n5\n")
	link(t, a, b)
	eventually(t, "newer wins on a", hasFile(a, "c/f.md", "1\n2\n3 from b\n4\n5\n"))
	eventually(t, "copy everywhere", func() bool {
		ca, cb := conflictCopies(a, "c", "f"), conflictCopies(b, "c", "f")
		for name, c := range ca {
			return len(ca) == 1 && len(cb) == 1 && cb[name] == c && c == "1\n2\n3 from a\n4\n5\n"
		}
		return false
	})
}

func TestS7_BinaryFilesAreNeverMerged(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	concurrentEdits(t, a, b, "c/f.bin", "1\x00\n2\n3\n4\n5\n", "1 from a\x00\n2\n3\n4\n5\n", "1\x00\n2\n3\n4\n5 from b\n")
	link(t, a, b)
	eventually(t, "copy on a", func() bool { return len(conflictCopies(a, "c", "f")) == 1 })
}

// A machine without the base content falls back while the other merges.
// The two outcomes must still converge, and neither edit may be lost.
func TestS7_MissingBaseOnOneSideStillConverges(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	concurrentEdits(t, a, b, "c/f.md", fiveLines, "1 from a\n2\n3\n4\n5\n", "1\n2\n3\n4\n5 from b\n")
	os.RemoveAll(filepath.Join(b.data, "history"))
	link(t, a, b)
	snap := func(m *machine) string { return dirSnapshot(m, "c") }
	eventually(t, "converged", func() bool { return snap(a) == snap(b) && strings.Contains(snap(a), "from a") })
	time.Sleep(300 * time.Millisecond)
	s := snap(a)
	if s != snap(b) || !strings.Contains(s, "1 from a") || !strings.Contains(s, "5 from b") {
		t.Fatalf("a:\n%s\nb:\n%s", s, snap(b))
	}
}

// dirSnapshot lists every file under ~/dir as "path=content".
func dirSnapshot(m *machine, dir string) string {
	var out []string
	root := m.path(dir)
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			rel, _ := filepath.Rel(root, p)
			b, _ := os.ReadFile(p)
			out = append(out, rel+"="+string(b))
		}
		return nil
	})
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func TestConflictListComesFromDisk(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	concurrentEdits(t, a, b, "c/f.md", "base\n", "on a\n", "on b\n")
	link(t, a, b)
	eventually(t, "copy listed on a", func() bool { return len(a.e.Status().Conflicts) == 1 })
	a.restart()
	if got := a.e.Status().Conflicts; len(got) != 1 || !strings.HasPrefix(got[0], "c/f.conflict-") {
		t.Fatalf("after restart: %v", got)
	}
	os.Remove(a.path(a.e.Status().Conflicts[0])) // the user resolved it
	a.e.Rescan()
	if got := a.e.Status().Conflicts; len(got) != 0 {
		t.Fatalf("after deleting the copy: %v", got)
	}
}

func TestConflictListIncludesSingleFileEntryCopies(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("cfg/rc", "base\n")
	a.add("cfg/rc")
	cut := link(t, a, b)
	eventually(t, "synced", hasFile(b, "cfg/rc", "base\n"))
	cut()
	a.writeAged("cfg/rc", "on a\n", time.Hour)
	a.e.Rescan()
	b.write("cfg/rc", "on b\n")
	b.e.Rescan()
	link(t, a, b)
	eventually(t, "copy listed on a", func() bool {
		got := a.e.Status().Conflicts
		return len(got) == 1 && strings.HasPrefix(got[0], "cfg/rc.conflict-")
	})
}
