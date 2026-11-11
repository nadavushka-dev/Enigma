package engine

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
)

// clock gives m's engine a time the test moves.
type clock struct{ t atomic.Int64 }

func (c *clock) now() time.Time      { return time.Unix(0, c.t.Load()) }
func (c *clock) add(d time.Duration) { c.t.Add(int64(d)) }

func withClock(c *clock) func(*Options) {
	return func(o *Options) { o.Now = c.now }
}

// ---- owner decision 1: identity strikes expire ------------------------------

// Identity changes with every file in place count only within 24 h: one
// a day, for days, never marks the entry unreliable.
func TestD15_IdentityStrikesExpireAfter24h(t *testing.T) {
	c := &clock{}
	c.t.Store(time.Now().UnixNano())
	var ino atomic.Uint64
	ino.Store(1)
	identity(t, func(id fsx.DirID) fsx.DirID { id.Ino = ino.Load(); return id })
	a, _ := notesFleet(t, noWatch, withClock(c))
	for day := 0; day < 5; day++ {
		c.add(25 * time.Hour)
		ino.Add(1)
		a.e.Rescan()
	}
	if st := a.e.Status().Entries[0]; st.IdentityUnreliable {
		t.Fatalf("one change a day marked the entry unreliable: %+v", st)
	}
	ino.Add(1)
	a.e.Rescan()
	ino.Add(1)
	a.e.Rescan() // three within 24 h (with the last daily one)
	if st := a.e.Status().Entries[0]; !st.IdentityUnreliable {
		t.Fatalf("three changes within 24 h did not: %+v", st)
	}
}

// Once unreliable, an entry stays so until 7 days pass with no further
// change of identity (a new one restarts the 7 days).
func TestD15_UnreliableClearsAfter7DaysWithoutAStrike(t *testing.T) {
	c := &clock{}
	c.t.Store(time.Now().UnixNano())
	var ino atomic.Uint64
	ino.Store(1)
	identity(t, func(id fsx.DirID) fsx.DirID { id.Ino = ino.Load(); return id })
	a, _ := notesFleet(t, noWatch, withClock(c))
	for i := 0; i < rootFlipLimit; i++ {
		ino.Add(1)
		a.e.Rescan()
	}
	unreliable := func() bool { return a.e.Status().Entries[0].IdentityUnreliable }
	if !unreliable() {
		t.Fatal("not unreliable after three changes")
	}
	c.add(6 * 24 * time.Hour)
	ino.Add(1) // a further change: the 7 days start again
	a.e.Rescan()
	c.add(6 * 24 * time.Hour)
	a.e.Rescan()
	if !unreliable() {
		t.Fatal("cleared 6 days after the last change")
	}
	c.add(25 * time.Hour)
	a.e.Rescan()
	if unreliable() {
		t.Fatal("still unreliable 7 days after the last change")
	}
}

// ---- owner decision 2: what happened while the daemon was off ----------------

func TestWhileAway_StartupSummarisesWhatItCouldNotSeeLive(t *testing.T) {
	var an notes
	a, b := notesFleet(t, noWatch, an.opt)
	a.e.Close()
	os.Remove(a.path("notes/a.md"))
	a.write("notes/b.md", "n2 edited offline")
	a.write("notes/c.md", "new offline")
	a.start()
	s := a.e.Status().Entries[0].WhileAway
	if s == nil || s.Kind != AwayNotRunning || s.Deleted != 1 || s.Changed != 1 || s.Added != 1 {
		t.Fatalf("summary %+v", s)
	}
	eventually(t, "a notification", an.has("notes", "while Enigma was not running"))
	// Visibility only: what syncs is unchanged.
	link(t, a, b)
	eventually(t, "the delete spreads as before", hasFile(b, "notes/a.md", "<missing>"))
}

func TestWhileAway_ReplacedFolderAtStartup(t *testing.T) {
	a, _ := notesFleet(t, noWatch)
	a.e.Close()
	os.RemoveAll(a.path("notes"))
	os.Mkdir(a.path("notes"), 0o755)
	a.start()
	s := a.e.Status().Entries[0].WhileAway
	if s == nil || !s.Replaced || s.Restoring != 2 || s.Deleted != 0 {
		t.Fatalf("summary %+v", s)
	}
}

func TestWhileAway_NothingToSayNoSummary(t *testing.T) {
	a, _ := notesFleet(t, noWatch)
	a.restart()
	if s := a.e.Status().Entries[0].WhileAway; s != nil {
		t.Fatalf("summary %+v after a quiet restart", s)
	}
}

func TestWhileAway_BackFromMissing(t *testing.T) {
	a, _ := notesFleet(t, noWatch)
	os.Rename(a.path("notes"), a.path("notes.old"))
	a.e.Rescan()
	os.Remove(a.path("notes.old/a.md"))
	os.Rename(a.path("notes.old"), a.path("notes"))
	a.e.Rescan()
	s := a.e.Status().Entries[0].WhileAway
	if s == nil || s.Kind != AwayMissing || s.Restoring != 1 {
		t.Fatalf("summary %+v", s)
	}
}

func TestWhileAway_BackInScope(t *testing.T) {
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
	a.write("notes/a.md", "edited while out")
	if err := b.e.GroupMembers(pair, []string{"id-a"}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a's summary", func() bool {
		s := a.e.Status().Entries[0].WhileAway
		return s != nil && s.Kind == AwayNotSynced && s.Changed == 1
	})
}

func TestWhileAway_ExpiresAfter24h(t *testing.T) {
	c := &clock{}
	c.t.Store(time.Now().UnixNano())
	a, _ := notesFleet(t, noWatch, withClock(c))
	a.e.Close()
	os.Remove(a.path("notes/a.md"))
	a.start()
	if a.e.Status().Entries[0].WhileAway == nil {
		t.Fatal("no summary")
	}
	c.add(25 * time.Hour)
	if s := a.e.Status().Entries[0].WhileAway; s != nil {
		t.Fatalf("still there after 24 h: %+v", s)
	}
}
