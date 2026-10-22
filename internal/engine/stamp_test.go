package engine

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
)

// countHashes makes m's engine count the files it reads to hash.
func countHashes(m *machine) *atomic.Int64 {
	var n atomic.Int64
	m.e.do(func() {
		m.e.hashStat = func(p string) (fsx.Info, error) {
			n.Add(1)
			return fsx.Stat(p)
		}
	})
	return &n
}

func noWatch(o *Options) { o.NoWatch = true }

// trustStamps lets m trust stamps at once (test files are written with an
// old mtime but a fresh ctime).
func trustStamps(m *machine) { m.e.do(func() { m.e.settle = 0 }) }

// Regression: every rescan hashed every file of every entry three times on
// the engine loop. A file whose size, mtime, inode and ctime are unchanged
// is not read again (SPEC §6.1).
func TestRescanDoesNotRereadUnchangedFiles(t *testing.T) {
	a := newMachine(t, "a", noWatch)
	trustStamps(a)
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		a.writeAged("d/"+n, "content "+n, time.Hour)
	}
	a.writeAged("solo.md", "solo", time.Hour)
	a.add("d")
	a.add("solo.md")
	hashes := countHashes(a)
	a.e.Rescan()
	a.e.Rescan()
	if n := hashes.Load(); n != 0 {
		t.Fatalf("rescans of unchanged files read %d files, want 0", n)
	}

	// a same-size edit with another (old) mtime is still seen
	a.writeAged("d/1", "CONTENT 1", 30*time.Minute)
	a.e.Rescan()
	if n := hashes.Load(); n != 1 {
		t.Fatalf("after one edit, %d files read, want 1", n)
	}
	var cur string
	a.e.do(func() { cur = a.e.st.Files["d/1"].V.Hash })
	if cur != fsx.Hash([]byte("CONTENT 1")) {
		t.Fatal("same-size edit not detected")
	}
}

// A file modified just now is not trusted to its stamp: an edit in the same
// timestamp tick would keep size and mtime (the "racy" case git guards).
func TestFreshFilesAreRehashed(t *testing.T) {
	a := newMachine(t, "a", noWatch)
	a.write("d/new", "fresh")
	a.add("d")
	hashes := countHashes(a)
	a.e.Rescan()
	a.e.Rescan()
	if n := hashes.Load(); n != 2 {
		t.Fatalf("a fresh file was read %d times over two rescans, want 2", n)
	}
}

// The scans never read a file over the size limit.
func TestRescanNeverReadsOversizedFiles(t *testing.T) {
	a := newMachine(t, "a", noWatch, func(o *Options) { o.MaxFileSize = 4 })
	trustStamps(a)
	a.writeAged("d/ok", "ok", time.Hour)
	a.writeAged("d/big", strings.Repeat("x", 10), time.Hour)
	a.add("d")
	hashes := countHashes(a)
	if err := os.Chmod(a.path("d/big"), 0); err != nil {
		t.Fatal(err)
	}
	a.e.Rescan()
	if n := hashes.Load(); n != 0 {
		t.Fatalf("%d files read, want 0", n)
	}
}
