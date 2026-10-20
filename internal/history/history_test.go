package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
)

func put(t *testing.T, s *Store, data string, age time.Duration) string {
	t.Helper()
	h, err := s.Put([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		old := time.Now().Add(-age)
		os.Chtimes(s.path(h), old, old)
	}
	return h
}

func TestPutGetByHash(t *testing.T) {
	s := Open(t.TempDir())
	h := put(t, s, "# rules", 0)
	if h != fsx.Hash([]byte("# rules")) {
		t.Fatalf("hash %s", h)
	}
	if got, ok := s.Get(h); !ok || string(got) != "# rules" {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, ok := s.Get(fsx.Hash([]byte("never stored"))); ok {
		t.Fatal("unknown hash found")
	}
	if _, err := s.Put([]byte("# rules")); err != nil { // idempotent
		t.Fatal(err)
	}
}

func TestEmptyContentIsStorable(t *testing.T) {
	s := Open(t.TempDir())
	h := put(t, s, "", 0)
	if got, ok := s.Get(h); !ok || len(got) != 0 {
		t.Fatalf("got %q %v", got, ok)
	}
}

func TestCorruptBlobIsNotReturned(t *testing.T) {
	s := Open(t.TempDir())
	h := put(t, s, "good", 0)
	os.Chmod(s.path(h), 0o600)
	os.WriteFile(s.path(h), []byte("bad"), 0o600)
	if _, ok := s.Get(h); ok {
		t.Fatal("a blob that does not match its hash must not be used as a merge base")
	}
}

func TestPruneKeepsRecentOrProtected(t *testing.T) {
	s := Open(t.TempDir())
	young := put(t, s, "young", time.Hour)
	oldProtected := put(t, s, "old but among the last 20", 40*24*time.Hour)
	oldLoose := put(t, s, "old and loose", 40*24*time.Hour)
	s.Prune(Policy{MaxAge: 30 * 24 * time.Hour, MaxBytes: 1 << 20}, map[string]bool{oldProtected: true}, nil)
	for h, want := range map[string]bool{young: true, oldProtected: true, oldLoose: false} {
		if _, ok := s.Get(h); ok != want {
			t.Errorf("%s kept=%v want %v", h[:6], ok, want)
		}
	}
}

func TestPruneCapsSizeOldestFirstButNeverCurrent(t *testing.T) {
	s := Open(t.TempDir())
	cur := put(t, s, "current-0123456789", 5*time.Hour)
	a := put(t, s, "oldest-0123456789", 4*time.Hour)
	b := put(t, s, "middle-0123456789", 3*time.Hour)
	c := put(t, s, "newest-0123456789", 2*time.Hour)
	protected := map[string]bool{cur: true, a: true, b: true, c: true}
	// Room for two 17-18 byte blobs: the oldest non-current ones go.
	s.Prune(Policy{MaxAge: 30 * 24 * time.Hour, MaxBytes: 40}, protected, map[string]bool{cur: true})
	for h, want := range map[string]bool{cur: true, a: false, b: false, c: true} {
		if _, ok := s.Get(h); ok != want {
			t.Errorf("%s kept=%v want %v", h[:6], ok, want)
		}
	}
}

func TestPruneDropsStrayTempFiles(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	put(t, s, "x", 0)
	stray := filepath.Join(dir, "objects", fsx.TempPrefix+"123")
	os.WriteFile(stray, []byte("half"), 0o600)
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(stray, old, old)
	s.Prune(DefaultPolicy, nil, nil)
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatal("stray temp file kept")
	}
}
