package engine

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// syncBuf is a log sink the test can read while the engine writes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Hand test 2, item 1: an entry root replaced by a symlink and then made a
// folder again leaves nothing listed as skipped (the root itself was).
func TestD15_RootLinkedAndBackLeavesNothingSkipped(t *testing.T) {
	a, b := notesFleet(t, noPoll)
	a.e.do(func() { // at once: the move alone is not what this is about
		os.Rename(a.path("notes"), a.path("dotnotes"))
		os.Symlink(a.path("dotnotes"), a.path("notes"))
	})
	a.e.do(a.e.pollRoots)
	a.e.do(func() {
		os.Remove(a.path("notes"))
		os.Rename(a.path("dotnotes"), a.path("notes"))
	})
	a.e.do(a.e.pollRoots)
	if st := a.e.Status().Entries[0]; st.Path != "notes" || len(st.Skipped) != 0 || st.Missing {
		t.Fatalf("status %+v, want nothing skipped", st)
	}
	settle(t, a, b)
	noDeleteReached(t, b)
}

// Hand test 2, items 2 and 3: a restore asked for before the folder went
// away again (the answer was lost) does not hold back the restore once the
// folder is back: the files come back at once, not after the retry
// interval. And the log does not speak of a link where there was none.
func TestD15_ReplacedRootIsRestoredPromptly(t *testing.T) {
	var logs syncBuf
	a, b := notesFleet(t, noWatch, func(o *Options) { o.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
	var drop atomic.Bool
	hook := func(machine, rel string) bool { return machine == "b" && drop.Load() }
	dropFetchHook.Store(&hook)
	t.Cleanup(func() { dropFetchHook.Store(nil) })
	drop.Store(true)
	os.RemoveAll(a.path("notes"))
	os.Mkdir(a.path("notes"), 0o755)
	a.e.Rescan() // a replaced folder: restores asked for, unanswered
	if st := a.e.Status().Entries[0]; len(st.Restoring) != 2 {
		t.Fatalf("status %+v, want 2 restoring", st)
	}
	os.RemoveAll(a.path("notes"))
	a.e.Rescan() // seen gone
	drop.Store(false)
	os.Mkdir(a.path("notes"), 0o755)
	a.e.Rescan()
	eventually(t, "a restored", func() bool { return bothFiles(a) })
	settle(t, a, b)
	noDeleteReached(t, b)
	if strings.Contains(logs.String(), "behind a link") {
		t.Fatalf("the log speaks of a link:\n%s", logs.String())
	}
}
