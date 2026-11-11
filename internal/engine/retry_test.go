package engine

import (
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// heldWrite reports, from m's loop, whether a failed write of rel is held
// and the peer it came from.
func heldWrite(t *testing.T, m *machine, rel string) (from string, ok bool) {
	t.Helper()
	if err := m.e.do(func() {
		if f := m.e.failing[rel]; f != nil {
			from, ok = f.p.name, true
		}
	}); err != nil {
		t.Fatal(err)
	}
	return from, ok
}

// heldBytes is how much file content a held write can keep: the size of
// its byte slices.
func heldBytes(f *failedWrite) int {
	n := 0
	v := reflect.ValueOf(f).Elem()
	for i := range v.NumField() {
		if fv := v.Field(i); fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() == reflect.Uint8 {
			n += fv.Len()
		}
	}
	return n
}

// Final review, should-fix: a failed write holds the version only, never
// its content (up to 2,000 files × 10 MB per entry would sit in memory);
// the retry fetches the content again from a peer.
func TestFailedWriteHoldsNoContentAndRefetches(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	a, b := newMachine(t, "a"), newMachine(t, "b", func(o *Options) { o.LinkPoll = 100 * time.Millisecond })
	a.write("n/perm/p.md", "v1")
	a.add("n")
	link(t, a, b)
	eventually(t, "synced", hasFile(b, "n/perm/p.md", "v1"))
	if err := os.Chmod(b.path("n/perm"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(b.path("n/perm"), 0o755) })
	var fetches atomic.Int32
	hook := func(machine, rel string) {
		if machine == "a" && rel == "n/perm/p.md" {
			fetches.Add(1)
		}
	}
	onFetchHook.Store(&hook)
	defer onFetchHook.Store(nil)
	a.write("n/perm/p.md", strings.Repeat("v2 ", 4096))
	eventually(t, "b holds the failing write", func() bool { _, ok := heldWrite(t, b, "n/perm/p.md"); return ok })
	n := -1
	b.e.do(func() { n = heldBytes(b.e.failing["n/perm/p.md"]) })
	if n != 0 {
		t.Fatalf("held write keeps %d content bytes, want 0", n)
	}
	before := fetches.Load()
	time.Sleep(writeRetryFirst + 500*time.Millisecond) // a retry fetches and fails again
	if fetches.Load() <= before {
		t.Fatal("the retry did not fetch the content again")
	}
	// The refetch that failed again is the same held write, backing off.
	var wait time.Duration
	b.e.do(func() {
		if f := b.e.failing["n/perm/p.md"]; f != nil {
			wait = f.wait
		}
	})
	if wait != 2*writeRetryFirst {
		t.Fatalf("wait after a failed retry is %v, want %v", wait, 2*writeRetryFirst)
	}
	before = fetches.Load()
	os.Chmod(b.path("n/perm"), 0o755)
	eventually(t, "written after the cause is fixed", hasFile(b, "n/perm/p.md", strings.Repeat("v2 ", 4096)))
	if fetches.Load() <= before {
		t.Fatal("written without fetching the content again")
	}
	eventually(t, "the report goes", func() bool { return len(b.e.Status().Entries[0].WriteFailing) == 0 })
}

// Final review, nit: a held write is retried through a peer that is still
// linked and shares the entry, not the one it came from once that one is
// gone; with no such peer it stays held and reported.
func TestFailedWriteRetriesThroughALinkedPeer(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	a := newMachine(t, "a")
	b := newMachine(t, "b", func(o *Options) { o.LinkPoll = 100 * time.Millisecond })
	c := newMachine(t, "c")
	a.write("n/perm/p.md", "v1")
	a.add("n")
	cuts := map[string]func(){"a": link(t, a, b), "c": link(t, c, b)}
	link(t, a, c)
	eventually(t, "synced to b", hasFile(b, "n/perm/p.md", "v1"))
	eventually(t, "synced to c", hasFile(c, "n/perm/p.md", "v1"))
	if err := os.Chmod(b.path("n/perm"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(b.path("n/perm"), 0o755) })
	a.write("n/perm/p.md", "v2")
	eventually(t, "c has v2", hasFile(c, "n/perm/p.md", "v2"))
	eventually(t, "b holds the failing write", func() bool { _, ok := heldWrite(t, b, "n/perm/p.md"); return ok })
	from, _ := heldWrite(t, b, "n/perm/p.md")
	cuts[from]()
	eventually(t, "b drops the link to "+from, func() bool { return len(b.e.Status().Peers) == 1 })
	time.Sleep(300 * time.Millisecond) // retries due while the source is gone
	if _, ok := heldWrite(t, b, "n/perm/p.md"); !ok {
		t.Fatal("held write dropped while another sharing peer is linked")
	}
	os.Chmod(b.path("n/perm"), 0o755)
	eventually(t, "written through the other peer", hasFile(b, "n/perm/p.md", "v2"))
}
