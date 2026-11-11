package engine

import (
	"os"
	"testing"
)

// Review 5, should-fix 2 (§6.5): a file entry whose parent folder is gone
// at start is not watched, so the parent is not created again; it is
// missing locally, and no delete is sent. When the folder is back, the
// file is restored.
func TestD15_FileEntryParentGoneIsNotRecreated(t *testing.T) {
	a, b := newMachine(t, "a", noPoll), newMachine(t, "b")
	a.write(".config/foo/settings.json", "s1")
	a.add(".config/foo/settings.json")
	link(t, a, b)
	eventually(t, "b has it", hasFile(b, ".config/foo/settings.json", "s1"))
	settle(t, a, b)
	a.e.Close()
	os.RemoveAll(a.path(".config/foo"))
	a.start()
	link(t, a, b)
	settle(t, a, b)
	if _, err := os.Lstat(a.path(".config/foo")); err == nil {
		t.Fatal("the gone parent folder was created again")
	}
	if b.read(".config/foo/settings.json") != "s1" || trashed(b, "s1") {
		t.Fatal("a delete reached the peer")
	}
	if st := a.e.Status().Entries[0]; !st.Missing {
		t.Fatalf("status %+v, want missing locally", st)
	}
	os.MkdirAll(a.path(".config/foo"), 0o755)
	a.e.do(a.e.pollRoots)
	eventually(t, "restored", hasFile(a, ".config/foo/settings.json", "s1"))
}
