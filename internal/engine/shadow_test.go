package engine

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// listDir is the names in dir, sorted; nil when dir is missing.
func listDir(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// Regression (D15, §6.6): Lstat only refuses to follow the last path
// component, so a symlinked folder inside an entry was read, written,
// given conflict copies and trashed through: Enigma edited the folder the
// link points at (a git repo, in the case that found it).
func TestD15_NeverWritesThroughSymlinkedParent(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	b.write("repo/sub/f.md", "repo")
	b.write("d/x.md", "x")
	if err := os.Symlink(b.path("repo/sub"), b.path("d/sub")); err != nil {
		t.Fatal(err)
	}
	a.write("d/x.md", "x")
	a.write("d/sub/f.md", "a-version")
	a.add("d")
	link(t, a, b)
	eventually(t, "b has the entry", func() bool { return len(b.e.Status().Entries) == 1 })
	time.Sleep(500 * time.Millisecond)
	a.write("d/sub/g.md", "new")
	time.Sleep(300 * time.Millisecond)
	os.Remove(a.path("d/sub/f.md"))
	time.Sleep(500 * time.Millisecond)

	if got := b.read("repo/sub/f.md"); got != "repo" {
		t.Fatalf("repo file through the link = %q, want untouched", got)
	}
	if got := listDir(t, b.path("repo/sub")); len(got) != 1 || got[0] != "f.md" {
		t.Fatalf("repo folder through the link = %v, want only f.md", got)
	}
	if fi, err := os.Lstat(b.path("d/sub")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link itself was replaced")
	}
	if trashed(b, "repo") {
		t.Fatal("the repo file was trashed through the link")
	}
	if got := a.read("d/sub/g.md"); got != "new" {
		t.Fatalf("a's own file = %q", got)
	}
	for _, n := range listDir(t, a.path("d/sub")) {
		if n != "g.md" {
			t.Fatalf("a got %s: content read through b's link spread", n)
		}
	}
}

// A synced folder replaced by a link on one machine: the files behind the
// link are neither read nor written, and no deletes are sent (§6.5 guard).
func TestD15_SymlinkedEntryRootIsNeverWrittenThrough(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("d/f.md", "one")
	a.add("d")
	link(t, a, b)
	eventually(t, "synced", hasFile(b, "d/f.md", "one"))

	a.write("repo/d/f.md", "repo")
	os.RemoveAll(a.path("d"))
	if err := os.Symlink(a.path("repo/d"), a.path("d")); err != nil {
		t.Fatal(err)
	}
	a.e.Rescan()
	b.write("d/f.md", "two")
	b.write("d/new.md", "new")
	time.Sleep(600 * time.Millisecond)

	if got := a.read("repo/d/f.md"); got != "repo" {
		t.Fatalf("repo file through the linked root = %q", got)
	}
	if got := listDir(t, a.path("repo/d")); len(got) != 1 {
		t.Fatalf("repo folder through the linked root = %v", got)
	}
	if b.read("d/f.md") != "two" || b.read("d/new.md") != "new" {
		t.Fatal("b lost its files: a sent deletes or the repo content")
	}
	if _, err := os.Stat(filepath.Join(a.data, "trash")); err == nil && trashed(a, "repo") {
		t.Fatal("trashed through the link")
	}
}
