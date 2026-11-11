package engine

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
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

func isSymlink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// moveBehindLink moves the folder rel of m to target (from ~) and leaves a
// symlink to it in its place, as a user moving a folder into a repo does.
// Both steps run on the engine's loop, so it sees them at once: an engine
// refresh landing between them would see the folder gone, which is a
// delete of what was moved (SPEC §6.6), not what these tests are about.
func moveBehindLink(t *testing.T, m *machine, rel, target string) {
	t.Helper()
	var err error
	m.e.do(func() {
		os.MkdirAll(filepath.Dir(m.path(target)), 0o755)
		if err = os.Rename(m.path(rel), m.path(target)); err == nil {
			err = os.Symlink(m.path(target), m.path(rel))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	m.e.Rescan()
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
	eventually(t, "b has the entry", func() bool { return hasEntry(b, "d") })
	settle(t, a, b)
	a.write("d/sub/g.md", "new")
	settle(t, a, b)
	os.Remove(a.path("d/sub/f.md"))
	settle(t, a, b)

	if got := b.read("repo/sub/f.md"); got != "repo" {
		t.Fatalf("repo file through the link = %q, want untouched", got)
	}
	if got := listDir(t, b.path("repo/sub")); len(got) != 1 || got[0] != "f.md" {
		t.Fatalf("repo folder through the link = %v, want only f.md", got)
	}
	if !isSymlink(b.path("d/sub")) {
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

// A synced folder entry whose root becomes a link on one machine: the
// entry is missing locally there (§6.5, §6.6), nothing behind the link is
// read or written, and no deletes are sent. When it is a folder again,
// syncing resumes and the files come back from the peers.
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
	if st := a.e.Status(); !st.Entries[0].Missing {
		t.Fatalf("a linked root is not missing locally: %+v", st.Entries[0])
	}
	b.write("d/f.md", "two")
	b.write("d/new.md", "new")
	settle(t, a, b)

	if got := a.read("repo/d/f.md"); got != "repo" {
		t.Fatalf("repo file through the linked root = %q", got)
	}
	if got := listDir(t, a.path("repo/d")); len(got) != 1 {
		t.Fatalf("repo folder through the linked root = %v", got)
	}
	if b.read("d/f.md") != "two" || b.read("d/new.md") != "new" {
		t.Fatal("b lost its files: a sent deletes or the repo content")
	}
	if trashed(a, "repo") || trashed(b, "two") {
		t.Fatal("trashed through the link, or a delete went out")
	}

	// A folder again: no longer missing, b's files come back, nothing is
	// deleted anywhere.
	os.Remove(a.path("d"))
	os.MkdirAll(a.path("d"), 0o755)
	a.e.Rescan()
	if a.e.Status().Entries[0].Missing {
		t.Fatal("still missing once the root is a folder again")
	}
	eventually(t, "a has b's edit back", hasFile(a, "d/f.md", "two"))
	eventually(t, "a has b's new file", hasFile(a, "d/new.md", "new"))
	settle(t, a, b)
	if b.read("d/f.md") != "two" || b.read("d/new.md") != "new" || trashed(b, "two") || trashed(b, "new") {
		t.Fatal("b lost files after the root came back")
	}
}

// Finding 1 (restore, never delete): a synced folder is moved into a repo
// and a symlink left in its place; later the symlink is removed. That
// absence is not a delete: every peer keeps its files, and they come
// back into the now-real folder, edits and new files made meanwhile
// included.
func TestD15_RemovingTheLinkRestoresNeverDeletes(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("d/x.md", "x")
	a.write("d/sub/f.md", "f1")
	a.write("d/sub/g.md", "g1")
	a.add("d")
	link(t, a, b)
	eventually(t, "b has sub", hasFile(b, "d/sub/g.md", "g1"))
	eventually(t, "b has sub", hasFile(b, "d/sub/f.md", "f1"))
	settle(t, a, b)

	moveBehindLink(t, b, "d/sub", "repo/sub")
	settle(t, a, b)
	if a.read("d/sub/f.md") != "f1" || a.read("d/sub/g.md") != "g1" {
		t.Fatal("a lost files when b put them behind a link")
	}
	a.write("d/sub/f.md", "f2 from a")    // an edit while shadowed on b
	a.write("d/sub/new.md", "new from a") // a new file while shadowed on b
	settle(t, a, b)
	if got := listDir(t, b.path("repo/sub")); len(got) != 2 || b.read("repo/sub/f.md") != "f1" {
		t.Fatalf("written through b's link: %v, f.md %q", got, b.read("repo/sub/f.md"))
	}

	os.Remove(b.path("d/sub")) // the link goes; the repo copy stays
	b.e.Rescan()
	eventually(t, "b restores f.md", hasFile(b, "d/sub/f.md", "f2 from a"))
	eventually(t, "b restores g.md", hasFile(b, "d/sub/g.md", "g1"))
	eventually(t, "b gets the new file", hasFile(b, "d/sub/new.md", "new from a"))
	settle(t, a, b)
	for rel, want := range map[string]string{"d/sub/f.md": "f2 from a", "d/sub/g.md": "g1", "d/sub/new.md": "new from a", "d/x.md": "x"} {
		if got := a.read(rel); got != want {
			t.Fatalf("a's %s = %q, want %q: a delete went out", rel, got, want)
		}
	}
	if trashed(a, "g1") || trashed(a, "f2 from a") || trashed(b, "g1") {
		t.Fatal("a removed link turned into a delete")
	}
	if isSymlink(b.path("d/sub")) || b.read("repo/sub/f.md") != "f1" {
		t.Fatal("the repo copy behind the old link changed")
	}
}

// The same across a restart: that the path is not on disk here any more
// is persisted, so a restart between the two steps cannot make a delete.
func TestD15_RemovingTheLinkAfterARestartRestores(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("d/x.md", "x")
	a.write("d/sub/f.md", "f1")
	a.add("d")
	link(t, a, b)
	eventually(t, "b has sub", hasFile(b, "d/sub/f.md", "f1"))
	moveBehindLink(t, b, "d/sub", "repo/sub")
	settle(t, a, b)

	b.restart()
	os.Remove(b.path("d/sub"))
	b.e.Rescan() // offline: the startup rescan of §6.5
	link(t, a, b)
	eventually(t, "b restores f.md", hasFile(b, "d/sub/f.md", "f1"))
	settle(t, a, b)
	if a.read("d/sub/f.md") != "f1" || trashed(a, "f1") {
		t.Fatal("a restart turned the removed link into a delete")
	}
}

// A preexisting copy (D7) restored after its link went: a's newer edit
// wins, and b's own copy is kept as a first-add backup, from history,
// since it is no longer on disk (D13).
func TestD13_RestoreOverAPreexistingCopyKeepsItFromHistory(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("d/x.md", "x")
	b.write("d/sub/extra.md", "b's extra")
	a.add("d")
	link(t, a, b)
	eventually(t, "a has b's extra", hasFile(a, "d/sub/extra.md", "b's extra"))

	moveBehindLink(t, b, "d/sub", "repo/sub")
	a.write("d/sub/extra.md", "a's edit")
	settle(t, a, b)
	os.Remove(b.path("d/sub"))
	b.e.Rescan()
	eventually(t, "b restores a's edit", hasFile(b, "d/sub/extra.md", "a's edit"))
	if !backedUp(b, "b's extra") {
		t.Fatal("b's preexisting copy was not kept")
	}
}
