package engine

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// replaceRoot puts an empty folder in the place of m's entry root, on its
// loop, without a refresh: as if no event said so.
func replaceRoot(t *testing.T, m *machine, rel string) {
	t.Helper()
	var err error
	m.e.do(func() {
		if err = os.Rename(m.path(rel), m.path(rel+".old")); err == nil {
			err = os.Mkdir(m.path(rel), 0o755)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A folder replaced while no peer is linked: status says the files are
// being restored (and a standing problem says so), not that all is well;
// once a peer is linked they come back and the problem goes.
func TestD15_StatusSaysRestoringUntilAPeerHasIt(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("notes/a.md", "n1")
	a.add("notes")
	cut := link(t, a, b)
	eventually(t, "b has it", hasFile(b, "notes/a.md", "n1"))
	cut()
	eventually(t, "down", func() bool { return len(a.e.Status().Peers) == 0 })
	replaceRoot(t, a, "notes")
	a.e.Rescan()
	st := a.e.Status().Entries[0]
	if st.Missing || len(st.Restoring) != 1 || st.Restoring[0] != "notes/a.md" {
		t.Fatalf("status %+v", st)
	}
	replaced := false
	for _, ev := range a.e.Events(0) {
		replaced = replaced || ev.Kind == EvRootReplaced && ev.Path == "notes" && strings.Contains(ev.Detail, "never deleted")
	}
	if !replaced {
		t.Fatal("no root-replaced event")
	}
	link(t, a, b)
	eventually(t, "restored", hasFile(a, "notes/a.md", "n1"))
	eventually(t, "no longer restoring", func() bool { return len(a.e.Status().Entries[0].Restoring) == 0 })
}

// Audit: a folder replaced while this machine is out of the entry's group
// (frozen, versions kept) is not read as deletes when it comes back in.
func TestD15_RootReplacedWhileFrozenIsRestoredOnReentry(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	knownFleet(t, a, b)
	pair, err := a.e.CreateGroup("pair", []string{"id-a", "id-b"})
	if err != nil {
		t.Fatal(err)
	}
	a.write("notes/a.md", "n1")
	if err := a.e.Add(a.path("notes"), nil, pair); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b has it", hasFile(b, "notes/a.md", "n1"))
	if err := b.e.GroupMembers(pair, nil, []string{"id-a"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a is out", func() bool { return a.e.Status().Entries[0].Frozen })
	replaceRoot(t, a, "notes")
	if err := b.e.GroupMembers(pair, []string{"id-a"}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a restores it", hasFile(a, "notes/a.md", "n1"))
	settle(t, a, b)
	if b.read("notes/a.md") != "n1" || trashed(b, "n1") {
		t.Fatal("re-entering the group sent a delete")
	}
}

// Audit: an entry removed and added again starts from the folder there
// now: no replaced-folder notice, nothing restored that is not wanted.
func TestD15_ReAddedEntryStartsFromItsFolder(t *testing.T) {
	a := newMachine(t, "a", func(o *Options) { o.NoWatch = true })
	a.write("notes/a.md", "n1")
	a.add("notes")
	if err := a.e.Remove(a.path("notes")); err != nil {
		t.Fatal(err)
	}
	os.Rename(a.path("notes"), a.path("notes.old"))
	a.write("notes/other.md", "o")
	a.add("notes")
	a.e.Rescan()
	for _, ev := range a.e.Events(0) {
		if ev.Kind == EvRootReplaced {
			t.Fatalf("a re-added entry was taken for a replaced folder: %+v", ev)
		}
	}
	if st := a.e.Status().Entries[0]; len(st.Restoring) != 0 || cur(a, "notes/a.md") != nil {
		t.Fatalf("the old entry's files came back: %+v", st)
	}
}

// A peer that does not have the file on disk serves it from its history
// store: two machines that both had their folder replaced restore each
// other.
func TestD15_RestoreFromAPeerThatHasItOnlyInHistory(t *testing.T) {
	a, b := newMachine(t, "a", func(o *Options) { o.NoWatch = true }), newMachine(t, "b", func(o *Options) { o.NoWatch = true })
	a.write("notes/a.md", "n1")
	a.add("notes")
	link(t, a, b)
	eventually(t, "b has it", hasFile(b, "notes/a.md", "n1"))
	replaceRoot(t, a, "notes")
	replaceRoot(t, b, "notes")
	a.e.Rescan() // a asks b, which has it in history only
	eventually(t, "a restored from b's history", hasFile(a, "notes/a.md", "n1"))
	b.e.Rescan()
	eventually(t, "b restored", hasFile(b, "notes/a.md", "n1"))
}

// A restore whose first peer does not answer asks the next one later, on
// its own (the poll tick), not only at the next rescan.
func TestD15_RestoreAsksTheNextPeerWhenOneDoesNotAnswer(t *testing.T) {
	quick := func(o *Options) { o.RestoreRetry = 300 * time.Millisecond; o.LinkPoll = 100 * time.Millisecond }
	a, b, c := newMachine(t, "a"), newMachine(t, "b", quick), newMachine(t, "c")
	a.write("notes/a.md", "n1")
	knownFleet(t, a, b, c)
	a.add("notes")
	eventually(t, "b has it", hasFile(b, "notes/a.md", "n1"))
	eventually(t, "c has it", hasFile(c, "notes/a.md", "n1"))
	settle(t, a, b, c)
	var askedA, askedC atomic.Int32
	drop := func(machine, rel string) bool {
		if rel != "notes/a.md" {
			return false
		}
		switch machine {
		case "a":
			askedA.Add(1)
			return true // a does not answer
		case "c":
			askedC.Add(1)
		}
		return false
	}
	dropFetchHook.Store(&drop)
	defer dropFetchHook.Store(nil)
	replaceRoot(t, b, "notes")
	b.e.do(func() { b.e.refreshUnder("notes", "notes") }) // one refresh: asks one peer
	eventually(t, "b restored from the next peer", hasFile(b, "notes/a.md", "n1"))
	if askedA.Load() == 0 || askedC.Load() == 0 {
		t.Fatalf("asked a %d, c %d times", askedA.Load(), askedC.Load())
	}
}

// shell runs fs steps back to back, as one shell line does: no engine
// work is ordered between them (the engine runs on its own).
func shell(t *testing.T, steps ...func() error) {
	t.Helper()
	for _, s := range steps {
		if err := s(); err != nil {
			t.Fatal(err)
		}
	}
}

// The sandbox repro (links off): the entry root is replaced by a symlink
// (cp, mv, ln on one line), the link and the moved folder are removed,
// the root is made an empty folder again, and the daemon restarts. The
// files must never be deleted on the peers, and must come back here.
func TestD15_RootSwappedForALinkThenRecreatedNeverDeletes(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("notes/a.md", "n1")
	a.write("notes/b.md", "n2")
	a.add("notes")
	link(t, a, b)
	eventually(t, "b has a.md", hasFile(b, "notes/a.md", "n1"))
	eventually(t, "b has b.md", hasFile(b, "notes/b.md", "n2"))

	home := a.home
	notes, old, repo := filepath.Join(home, "notes"), filepath.Join(home, "notes.old"), filepath.Join(home, "repo", "notes")
	shell(t,
		func() error { return os.MkdirAll(filepath.Dir(repo), 0o755) },
		func() error { return os.CopyFS(repo, os.DirFS(notes)) },
		func() error { return os.Rename(notes, old) },
		func() error { return os.Symlink(repo, notes) })
	time.Sleep(500 * time.Millisecond) // the watcher's turn, as a user's pause
	shell(t,
		func() error { return os.Remove(notes) },
		func() error { return os.RemoveAll(old) })
	time.Sleep(500 * time.Millisecond)
	if b.read("notes/a.md") != "n1" || b.read("notes/b.md") != "n2" {
		t.Fatal("b lost files while the root was a link or absent")
	}

	if err := os.Mkdir(notes, 0o755); err != nil { // an empty, real folder again
		t.Fatal(err)
	}
	eventually(t, "a restores a.md without a restart", hasFile(a, "notes/a.md", "n1"))
	eventually(t, "a restores b.md", hasFile(a, "notes/b.md", "n2"))

	a.restart()
	link(t, a, b)
	settle(t, a, b)
	for rel, want := range map[string]string{"notes/a.md": "n1", "notes/b.md": "n2"} {
		if got := b.read(rel); got != want || trashed(b, want) {
			t.Fatalf("b's %s = %q (trashed %v): a delete went out", rel, got, trashed(b, want))
		}
		if got := a.read(rel); got != want {
			t.Fatalf("a's %s = %q after the restart", rel, got)
		}
	}
}

// A root that is gone when the daemon starts (an unmounted disk, a
// removed link) is not re-created by starting to watch it, and its files
// are not deleted anywhere; when it is back, so are the files.
func TestD15_AbsentRootAtStartupIsNotRecreated(t *testing.T) {
	for _, how := range []string{"moved-away", "was-a-link"} {
		t.Run(how, func(t *testing.T) {
			a, b := newMachine(t, "a"), newMachine(t, "b")
			a.write("notes/a.md", "n1")
			a.add("notes")
			link(t, a, b)
			eventually(t, "b has it", hasFile(b, "notes/a.md", "n1"))
			if how == "was-a-link" {
				moveBehindLink(t, a, "notes", "repo/notes")
				settle(t, a, b)
			}
			a.e.Close()
			if how == "was-a-link" {
				os.Remove(a.path("notes"))
			} else {
				os.Rename(a.path("notes"), filepath.Join(filepath.Dir(a.home), "unmounted"))
			}
			a.start()
			link(t, a, b)
			settle(t, a, b)
			if _, err := os.Lstat(a.path("notes")); !os.IsNotExist(err) {
				t.Fatalf("the absent root was re-created: %v", listDir(t, a.path("notes")))
			}
			if b.read("notes/a.md") != "n1" || trashed(b, "n1") {
				t.Fatal("a delete went out")
			}
			if !a.e.Status().Entries[0].Missing {
				t.Fatal("not reported missing locally")
			}
			if how == "moved-away" {
				os.Rename(filepath.Join(filepath.Dir(a.home), "unmounted"), a.path("notes"))
			} else {
				os.Mkdir(a.path("notes"), 0o755)
			}
			eventually(t, "a has it again", hasFile(a, "notes/a.md", "n1"))
			eventually(t, "no longer missing", func() bool { return !a.e.Status().Entries[0].Missing })
		})
	}
}

// The same with the daemon restarted while the root is still an empty
// new folder: the startup scan must not read the missing files as deletes.
func TestD15_RecreatedRootAtStartupNeverDeletes(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("notes/a.md", "n1")
	a.write("notes/b.md", "n2")
	a.add("notes")
	link(t, a, b)
	eventually(t, "b has both", func() bool { return b.read("notes/a.md") == "n1" && b.read("notes/b.md") == "n2" })
	settle(t, a, b)

	a.e.Close()
	notes := a.path("notes")
	shell(t,
		func() error { return os.Rename(notes, a.path("notes.old")) },
		func() error { return os.Symlink(a.path("notes.old"), notes) },
		func() error { return os.Remove(notes) },
		func() error { return os.RemoveAll(a.path("notes.old")) },
		func() error { return os.Mkdir(notes, 0o755) })
	a.start()
	link(t, a, b)
	settle(t, a, b)
	if b.read("notes/a.md") != "n1" || b.read("notes/b.md") != "n2" || trashed(b, "n1") || trashed(b, "n2") {
		t.Fatal("a delete went out")
	}
	eventually(t, "a restores a.md", hasFile(a, "notes/a.md", "n1"))
	eventually(t, "a restores b.md", hasFile(a, "notes/b.md", "n2"))
}
