package engine

import (
	"os"
	"sync/atomic"
	"testing"
)

// localPath delivers one file event for rel to m, as the watcher does
// (m runs without a watcher, so nothing else rescans it).
func localPath(m *machine, rel string) {
	m.e.do(func() { m.e.onLocalPath(m.path(rel)) })
}

// Review #2 A: the entry root comes back as a folder and only a file
// event says so (no rescan). The entry is no longer missing, the changes
// peers made meanwhile are restored, and so is a file a peer added while
// the root was a symlink.
func TestD15_LinkedRootBackByAFileEventRestores(t *testing.T) {
	a, b := newMachine(t, "a", func(o *Options) { o.NoWatch = true }), newMachine(t, "b")
	a.write("d/f.md", "one")
	a.add("d")
	link(t, a, b)
	eventually(t, "synced", hasFile(b, "d/f.md", "one"))

	moveBehindLink(t, a, "d", "repo/d")
	localPath(a, "d")
	if !a.e.Status().Entries[0].Missing {
		t.Fatal("the linked root is not missing locally")
	}
	b.write("d/f.md", "two")
	b.write("d/new.md", "added while a's root was a link")
	b.e.Rescan()
	settle(t, a, b)
	if a.read("repo/d/f.md") != "one" || len(listDir(t, a.path("repo/d"))) != 1 {
		t.Fatal("written through the linked root")
	}

	os.Remove(a.path("d"))
	os.MkdirAll(a.path("d"), 0o755)
	localPath(a, "d") // the only notice a gets
	eventually(t, "a has b's edit", hasFile(a, "d/f.md", "two"))
	eventually(t, "a has b's new file", hasFile(a, "d/new.md", "added while a's root was a link"))
	if a.e.Status().Entries[0].Missing {
		t.Fatal("still missing after the root came back")
	}
}

// Review #2 B: every file of an entry was behind its linked root; the
// root link is then removed for good (here while the daemon is stopped,
// so nothing in memory remembers it was missing). The root is not
// re-created and nothing is written or deleted anywhere; once the user
// makes it a folder again, the files come back.
func TestD15_RemovedLinkedRootIsNotRecreated(t *testing.T) {
	a, b := newMachine(t, "a", func(o *Options) { o.NoWatch = true }), newMachine(t, "b")
	a.write("d/f.md", "f")
	a.write("d/g.md", "g")
	a.add("d")
	link(t, a, b)
	eventually(t, "synced", hasFile(b, "d/g.md", "g"))
	eventually(t, "synced", hasFile(b, "d/f.md", "f"))
	moveBehindLink(t, a, "d", "repo/d")
	settle(t, a, b)

	a.e.Close()
	os.Remove(a.path("d")) // gone for good (a drive unplugged, decommissioned)
	a.start()
	link(t, a, b)
	settle(t, a, b)
	if _, err := os.Lstat(a.path("d")); !os.IsNotExist(err) {
		t.Fatalf("the removed root was re-created: %v", listDir(t, a.path("d")))
	}
	if b.read("d/f.md") != "f" || b.read("d/g.md") != "g" || trashed(b, "f") || trashed(b, "g") {
		t.Fatal("a delete went out")
	}
	if !a.e.Status().Entries[0].Missing {
		t.Fatal("the entry is not reported missing locally")
	}

	os.MkdirAll(a.path("d"), 0o755)
	a.e.Rescan()
	eventually(t, "f back", hasFile(a, "d/f.md", "f"))
	eventually(t, "g back", hasFile(a, "d/g.md", "g"))
}

// The same without a restart, by file events: a file a peer added while
// the root was a link is still fetched once the user makes the root a
// folder again, not lost when the link was removed.
func TestD15_RemovedLinkedRootKeepsWhatPeersAdded(t *testing.T) {
	a, b := newMachine(t, "a", func(o *Options) { o.NoWatch = true }), newMachine(t, "b")
	a.write("d/f.md", "f")
	a.add("d")
	link(t, a, b)
	eventually(t, "synced", hasFile(b, "d/f.md", "f"))
	moveBehindLink(t, a, "d", "repo/d")
	localPath(a, "d")
	b.write("d/new.md", "new")
	b.e.Rescan()
	settle(t, a, b)

	os.Remove(a.path("d"))
	localPath(a, "d")
	settle(t, a, b)
	if _, err := os.Lstat(a.path("d")); !os.IsNotExist(err) {
		t.Fatal("the removed root was re-created")
	}
	os.MkdirAll(a.path("d"), 0o755)
	localPath(a, "d")
	eventually(t, "f back", hasFile(a, "d/f.md", "f"))
	eventually(t, "b's new file arrives", hasFile(a, "d/new.md", "new"))
}

// Each guard against re-creating a removed root, on its own: with the
// root gone and nothing in memory saying it is missing (a restart), no
// restore is wanted or asked for, writing counts as re-creating a root,
// and what peers changed meanwhile is kept for later.
func TestD15_RestoreGuardsWithoutTheRoot(t *testing.T) {
	a, b := newMachine(t, "a", func(o *Options) { o.NoWatch = true }), newMachine(t, "b")
	a.write("d/f.md", "f")
	a.add("d")
	link(t, a, b)
	eventually(t, "synced", hasFile(b, "d/f.md", "f"))
	moveBehindLink(t, a, "d", "repo/d")
	settle(t, a, b)
	os.Remove(a.path("d"))

	var asked atomic.Int32
	hook := func(machine, rel string) {
		if machine == "b" && rel == "d/f.md" {
			asked.Add(1)
		}
	}
	onFetchHook.Store(&hook)
	defer onFetchHook.Store(nil)
	var needs, vanished bool
	var kept int
	a.e.do(func() {
		delete(a.e.missing, "d") // as after a restart
		needs = a.e.needsRestore("d", "d/f.md")
		vanished = a.e.rootVanished("d")
		a.e.requestRestore("d", "d/f.md")
		a.e.shadowIn["d/new.md"] = true
		a.e.restoreShadowIn("d", "d")
		kept = len(a.e.shadowIn)
	})
	settle(t, a, b)
	if needs {
		t.Error("needsRestore with the root gone")
	}
	if !vanished {
		t.Error("rootVanished is false although a's version of d/f.md is kept")
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("a asked for a restore %d times with the root gone", n)
	}
	if kept != 1 {
		t.Error("what a peer changed meanwhile was dropped")
	}
}

// Review #2 nit: a restore asks one peer, not every peer.
func TestD15_RestoreAsksOnePeer(t *testing.T) {
	a, b, c := newMachine(t, "a"), newMachine(t, "b"), newMachine(t, "c")
	a.write("d/x.md", "x")
	a.write("d/sub/f.md", "f")
	knownFleet(t, a, b, c)
	a.add("d")
	for _, m := range []*machine{b, c} {
		eventually(t, m.name+" has f", hasFile(m, "d/sub/f.md", "f"))
	}
	moveBehindLink(t, b, "d/sub", "repo/sub")
	settle(t, a, b, c)

	var served atomic.Int32
	hook := func(machine, rel string) {
		if rel == "d/sub/f.md" && machine != "b" {
			served.Add(1)
		}
	}
	onFetchHook.Store(&hook)
	defer onFetchHook.Store(nil)
	os.Remove(b.path("d/sub"))
	b.e.Rescan()
	eventually(t, "b restores f", hasFile(b, "d/sub/f.md", "f"))
	settle(t, a, b, c)
	if n := served.Load(); n != 1 {
		t.Fatalf("%d peers sent the file for one restore, want 1", n)
	}
}
