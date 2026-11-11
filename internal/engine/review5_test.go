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

// Review 5, nit: a delete recorded for a path behind a symlinked folder
// here is still announced, so a machine that hears only through this one
// learns of it at once.
func TestD15_ShadowedDeleteIsRelayed(t *testing.T) {
	a, b, c := newMachine(t, "a"), newMachine(t, "b"), newMachine(t, "c")
	a.write("d/x.md", "x")
	a.write("d/sub/f.md", "f")
	a.add("d")
	link(t, a, b)
	link(t, b, c)
	eventually(t, "c has it", hasFile(c, "d/sub/f.md", "f"))
	settle(t, a, b, c)
	moveBehindLink(t, b, "d/sub", "repo/sub")
	settle(t, a, b, c)
	os.Remove(a.path("d/sub/f.md"))
	a.e.Rescan()
	eventually(t, "the delete reaches c through b", hasFile(c, "d/sub/f.md", "<missing>"))
	if b.read("repo/sub/f.md") != "f" {
		t.Fatal("the delete went through b's link")
	}
}

// Review 5, nit (S5): with links off, a tracked link replaced by a regular
// file is an edit like any other: it reaches the peers whose current
// version is the link, and their link goes to the trash.
func TestD23_LinksOffLinkReplacedByAFileSyncs(t *testing.T) {
	a, b := skillsFleet(t)
	eventually(t, "link on b", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	if err := a.e.SetLinks(a.path(".agents/skills"), false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b has links off", func() bool {
		for _, es := range b.e.Status().Entries {
			if es.Path == ".agents/skills" {
				return !es.LinksOn
			}
		}
		return false
	})
	os.Remove(a.path(".agents/skills/foo"))
	a.write(".agents/skills/foo", "now a file")
	a.e.Rescan()
	eventually(t, "the file reaches b", hasFile(b, ".agents/skills/foo", "now a file"))
	if !linkTrashed(b, b.path("repo/skills/foo")) {
		t.Fatal("b's link is not in its trash")
	}
	if b.read("repo/skills/foo/SKILL.md") != "foo" {
		t.Fatal("the link's target changed")
	}
}
