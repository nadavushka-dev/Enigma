package engine

import (
	"os"
	"sync/atomic"
	"testing"

	"github.com/nadavushka-dev/enigma/internal/fsx"
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

// Review 5, nit: the device number drifting (NFSv3, once per boot) where
// no birth time is known: the same inode is the same folder, so a file
// deleted while the daemon was down is deleted everywhere, not restored.
func TestD15_DeviceDriftWithoutBirthTimeIsTheSameFolder(t *testing.T) {
	var boot atomic.Uint64
	boot.Store(1)
	identity(t, func(id fsx.DirID) fsx.DirID { id.Dev, id.Btime = boot.Load(), 0; return id })
	a, b := notesFleet(t, noWatch)
	a.e.Close()
	boot.Add(1)
	os.Remove(a.path("notes/a.md"))
	a.start()
	link(t, a, b)
	eventually(t, "the delete spreads", hasFile(b, "notes/a.md", "<missing>"))
	if a.read("notes/a.md") != "<missing>" {
		t.Fatal("the deleted file was restored")
	}
}

// Review 5, nit: an identity that changes once per boot (a remount that
// renumbers inodes) with every file in place reaches the limit across
// restarts: the count is kept with the state.
func TestD15_IdentityFlipsAreCountedAcrossRestarts(t *testing.T) {
	var boot atomic.Uint64
	boot.Store(1)
	identity(t, func(id fsx.DirID) fsx.DirID { id.Ino = boot.Load(); return id })
	a, _ := notesFleet(t, noWatch)
	for i := 0; i < rootFlipLimit; i++ {
		a.e.Close()
		boot.Add(1)
		a.start()
	}
	if st := a.e.Status().Entries[0]; !st.IdentityUnreliable {
		t.Fatalf("status %+v after %d boots with a new identity", st, rootFlipLimit)
	}
}
