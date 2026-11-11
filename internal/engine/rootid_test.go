package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
)

// identity replaces how folders are identified for the rest of the test.
func identity(t *testing.T, f func(real fsx.DirID) fsx.DirID) {
	t.Helper()
	old := dirIDOf
	dirIDOf = func(p string) (fsx.DirID, bool) {
		id, ok := old(p)
		if !ok {
			return id, ok
		}
		return f(id), true
	}
	t.Cleanup(func() { dirIDOf = old })
}

// reuseInodes stands in for Linux handing a re-created folder the inode
// of the one just deleted: device and inode always match.
func reuseInodes(t *testing.T) {
	identity(t, func(id fsx.DirID) fsx.DirID { id.Dev, id.Ino = 1, 1; return id })
}

// collideAll: no identity tells two folders apart (no birth time either).
func collideAll(t *testing.T) {
	identity(t, func(fsx.DirID) fsx.DirID { return fsx.DirID{Dev: 1, Ino: 1} })
}

func bothFiles(m *machine) bool {
	return m.read("notes/a.md") == "n1" && m.read("notes/b.md") == "n2"
}

func notesFleet(t *testing.T, opts ...func(*Options)) (a, b *machine) {
	t.Helper()
	a, b = newMachine(t, "a", opts...), newMachine(t, "b")
	a.write("notes/a.md", "n1")
	a.write("notes/b.md", "n2")
	a.add("notes")
	link(t, a, b)
	eventually(t, "b has both", func() bool { return bothFiles(b) })
	settle(t, a, b)
	return a, b
}

func noDeleteReached(t *testing.T, b *machine) {
	t.Helper()
	if !bothFiles(b) || trashed(b, "n1") || trashed(b, "n2") {
		t.Fatal("a delete reached the peer")
	}
}

// F1 (hand test A1, A2b): the folder re-created at once gets the deleted
// one's inode back; its birth time tells them apart. Restore, no delete.
func TestD15_InodeReusedRootIsRestored(t *testing.T) {
	reuseInodes(t)
	a, b := notesFleet(t, noWatch)
	os.RemoveAll(a.path("notes"))
	os.Mkdir(a.path("notes"), 0o755)
	a.e.Rescan()
	settle(t, a, b)
	noDeleteReached(t, b)
	eventually(t, "a restored", func() bool { return bothFiles(a) })
}

// F1 (hand test A16): a root seen absent is never accepted back as the
// same folder, whatever its identity says.
func TestD15_RootSeenAbsentComesBackAsAReplacement(t *testing.T) {
	collideAll(t)
	a, b := notesFleet(t, noWatch)
	os.Rename(a.path("notes"), a.path("notes.old"))
	a.e.Rescan() // seen absent
	os.RemoveAll(a.path("notes.old"))
	os.Mkdir(a.path("notes"), 0o755)
	a.e.Rescan()
	settle(t, a, b)
	noDeleteReached(t, b)
	eventually(t, "a restored", func() bool { return bothFiles(a) })
}

// F1 and F2 (hand test A1): removed and re-created on one line while the
// daemon runs, nothing telling the folders apart. The root's own removal
// event says it was replaced: restore, no delete; and the new folder is
// watched, so a file written in it syncs without a restart.
func TestD15_RootRemovedAndRecreatedWhileRunning(t *testing.T) {
	collideAll(t)
	a, b := notesFleet(t)
	os.RemoveAll(a.path("notes"))
	os.Mkdir(a.path("notes"), 0o755)
	eventually(t, "a restored", func() bool { return bothFiles(a) })
	a.write("notes/new.md", "after")
	eventually(t, "the new folder syncs", hasFile(b, "notes/new.md", "after"))
	settle(t, a, b)
	noDeleteReached(t, b)
	if st := a.e.Status().Entries[0]; st.Unwatched {
		t.Fatalf("status says the entry is not watched: %+v", st)
	}
}

// F2 backstop: a folder whose watch died without an event (watcher
// trouble) shows as not watched in status, and the root poll watches it
// again, so its changes sync without a restart.
func TestD15_DeadWatchIsWatchedAgain(t *testing.T) {
	a, b := notesFleet(t, func(o *Options) { o.LinkPoll = time.Hour }) // no poll until asked
	a.e.do(func() { a.e.w.Remove(a.path("notes")) })
	if st := a.e.Status().Entries[0]; !st.Unwatched {
		t.Fatalf("status says watched: %+v", st)
	}
	a.e.do(a.e.pollRoots)
	if st := a.e.Status().Entries[0]; st.Unwatched {
		t.Fatalf("still not watched after the poll: %+v", st)
	}
	a.write("notes/new.md", "after")
	eventually(t, "a file event syncs it", hasFile(b, "notes/new.md", "after"))
	settle(t, a, b)
	noDeleteReached(t, b)
}

// Review #4: an entry with files on disk and no recorded folder (state
// from before identities) whose root is first seen absent: the first real
// folder that appears is a replacement.
func TestD15_UpgradeWithoutARecordedRoot(t *testing.T) {
	collideAll(t)
	a, b := notesFleet(t, noWatch)
	a.e.Close()
	p := filepath.Join(a.data, "state.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]json.RawMessage
	json.Unmarshal(raw, &st)
	delete(st, "roots")
	raw, _ = json.Marshal(st)
	os.WriteFile(p, raw, 0o600)
	os.RemoveAll(a.path("notes"))
	a.start()
	link(t, a, b)
	settle(t, a, b)
	os.Mkdir(a.path("notes"), 0o755)
	a.e.Rescan()
	settle(t, a, b)
	noDeleteReached(t, b)
	eventually(t, "a restored", func() bool { return bothFiles(a) })
}

// Review #2: a file entry whose parent folder is a symlink (dotfiles) is
// watched as before: an edit syncs without a rescan.
func TestD15_FileEntryUnderASymlinkedFolderIsWatched(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("dotfiles/config/settings.json", "v1")
	a.symlink(".config", a.path("dotfiles/config"))
	a.add(".config/settings.json")
	link(t, a, b)
	eventually(t, "b has it", hasFile(b, ".config/settings.json", "v1"))
	a.write("dotfiles/config/settings.json", "v2")
	eventually(t, "the edit syncs from a file event", hasFile(b, ".config/settings.json", "v2"))
}

// Review #3: the device number changing on its own (btrfs, NFS, external
// volumes) with the same inode and birth time is the same folder: a
// file the user deletes is still deleted everywhere.
func TestD15_DeviceDriftIsTheSameFolder(t *testing.T) {
	var n atomic.Uint64
	identity(t, func(id fsx.DirID) fsx.DirID { id.Dev = n.Add(1); return id })
	a, b := notesFleet(t, noWatch)
	os.Remove(a.path("notes/a.md"))
	a.e.Rescan()
	eventually(t, "the delete spreads", hasFile(b, "notes/a.md", "<missing>"))
	for _, ev := range a.e.Events(0) {
		if ev.Kind == EvRootReplaced {
			t.Fatalf("device drift taken for a replaced folder: %+v", ev)
		}
	}
}

// Review #3: an identity that changes all the time (FUSE without stable
// inodes) is not relied on for that entry, and a standing status says so;
// deletes there spread again.
func TestD15_FlappingIdentityIsNotReliedOn(t *testing.T) {
	var n atomic.Uint64
	identity(t, func(id fsx.DirID) fsx.DirID { id.Ino = n.Add(1); return id })
	a, b := notesFleet(t, noWatch)
	for i := 0; i < 5; i++ {
		a.e.Rescan()
	}
	if st := a.e.Status().Entries[0]; !st.IdentityUnreliable {
		t.Fatalf("status %+v", st)
	}
	os.Remove(a.path("notes/a.md"))
	a.e.Rescan()
	eventually(t, "the delete spreads", hasFile(b, "notes/a.md", "<missing>"))
}
