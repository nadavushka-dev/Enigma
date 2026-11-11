package engine

import (
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/watch"
)

// noPoll leaves the root poll to the test (a.e.do(a.e.pollRoots)).
func noPoll(o *Options) { o.LinkPoll = time.Hour }

// failWatch makes every watch of dir fail (the inotify watch limit,
// ENOSPC) while fail is set, and counts the attempts.
func failWatch(t *testing.T, dir func() string, fail *atomic.Bool) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	t.Cleanup(watch.SetAddHook(func(d string) error {
		if d != dir() {
			return nil
		}
		n.Add(1)
		if fail.Load() {
			return syscall.ENOSPC
		}
		return nil
	}))
	return &n
}

// dueWatchRetry makes entry's next watch retry due now.
func dueWatchRetry(m *machine, entry string) {
	m.e.do(func() {
		if rt := m.e.watchRetry[entry]; rt != nil {
			rt.next = time.Time{}
		}
	})
}

// Review 5, should-fix 1: a root watch that failed to add (the watch
// limit) never died, so the folder is not another one: the root poll does
// not mark its files as not on disk here (a delete made meanwhile is a
// delete, not a restore), and the watch is retried with a backoff, not on
// every poll. Status says it is not watched until it is.
func TestD15_RootWatchThatFailsToAddIsRetriedNotAReplacement(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	a, b := newMachine(t, "a", noPoll), newMachine(t, "b")
	tries := failWatch(t, func() string { return a.path("notes") }, &fail)
	a.write("notes/a.md", "n1")
	a.write("notes/b.md", "n2")
	a.add("notes")
	link(t, a, b)
	eventually(t, "b has both", func() bool { return bothFiles(b) })
	settle(t, a, b)
	if st := a.e.Status().Entries[0]; !st.Unwatched {
		t.Fatalf("status says watched: %+v", st)
	}
	before := tries.Load()
	os.Remove(a.path("notes/a.md"))
	for i := 0; i < 5; i++ {
		a.e.do(a.e.pollRoots)
	}
	if n := tries.Load() - before; n > 1 {
		t.Fatalf("the watch was tried %d times in 5 polls, want a backoff", n)
	}
	a.e.Rescan()
	eventually(t, "the delete spreads", hasFile(b, "notes/a.md", "<missing>"))
	if a.read("notes/a.md") != "<missing>" {
		t.Fatal("the deleted file was restored on a")
	}
	fail.Store(false)
	dueWatchRetry(a, "notes")
	a.e.do(a.e.pollRoots)
	if st := a.e.Status().Entries[0]; st.Unwatched {
		t.Fatalf("still not watched after a retry: %+v", st)
	}
	a.write("notes/new.md", "after")
	eventually(t, "a file event syncs it", hasFile(b, "notes/new.md", "after"))
}

// Review 5, item 5: a subfolder whose watch failed is reported; status is
// never "ok" for a folder entry that is only partly watched.
func TestD15_SubfolderWatchThatFailsIsReported(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	a := newMachine(t, "a", noPoll)
	failWatch(t, func() string { return a.path("notes/sub") }, &fail)
	a.write("notes/sub/x.md", "x")
	a.add("notes")
	st := a.e.Status().Entries[0]
	if st.Unwatched || len(st.UnwatchedFolders) != 1 || st.UnwatchedFolders[0] != "notes/sub" {
		t.Fatalf("status %+v, want notes/sub not watched", st)
	}
	fail.Store(false)
	a.e.do(a.e.pollRoots) // first sight: the retry waits
	dueWatchRetry(a, "notes")
	a.e.do(a.e.pollRoots)
	if st := a.e.Status().Entries[0]; len(st.UnwatchedFolders) != 0 {
		t.Fatalf("still reported after a retry: %+v", st)
	}
}

// Review 5, should-fix 3: an entry whose identity is not relied on is
// polled for what is at its root (a folder, a link, nothing), not for its
// identity: no re-watch and no walk of the tree on every poll.
func TestD15_UnreliableIdentityIsNotRewatchedEveryPoll(t *testing.T) {
	var n atomic.Uint64
	identity(t, func(id fsx.DirID) fsx.DirID { id.Ino = n.Add(1); return id })
	var never atomic.Bool
	a, b := newMachine(t, "a", noPoll), newMachine(t, "b")
	tries := failWatch(t, func() string { return a.path("notes") }, &never)
	a.write("notes/a.md", "n1")
	a.write("notes/b.md", "n2")
	a.add("notes")
	link(t, a, b)
	eventually(t, "b has both", func() bool { return bothFiles(b) })
	for i := 0; i < 5; i++ {
		a.e.Rescan()
	}
	if st := a.e.Status().Entries[0]; !st.IdentityUnreliable {
		t.Fatalf("status %+v", st)
	}
	a.e.do(a.e.pollRoots)
	before := tries.Load()
	for i := 0; i < 5; i++ {
		a.e.do(a.e.pollRoots)
	}
	if got := tries.Load() - before; got != 0 {
		t.Fatalf("watched again %d times in 5 polls", got)
	}
}
