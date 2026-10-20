package watch

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const debounce = 40 * time.Millisecond

func newW(t *testing.T) *Watcher {
	t.Helper()
	w, err := New(debounce)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// collect gathers every path reported within d.
func collect(w *Watcher, d time.Duration) (paths []string, batches int) {
	deadline := time.After(d)
	for {
		select {
		case b := <-w.Events():
			batches++
			paths = append(paths, b.Paths...)
		case <-deadline:
			return paths, batches
		}
	}
}

func waitFor(t *testing.T, w *Watcher, want string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case b := <-w.Events():
			if slices.Contains(b.Paths, want) {
				return
			}
		case <-deadline:
			t.Fatalf("no event for %s", want)
		}
	}
}

func real(t *testing.T, p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReportsChangesInNewSubdirectories(t *testing.T) {
	root := real(t, t.TempDir())
	w := newW(t)
	if err := w.Add(root, false, nil); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	time.Sleep(3 * debounce) // let the new directories be picked up
	f := filepath.Join(root, "a", "b", "new.md")
	write(t, f, "x")
	waitFor(t, w, f)
}

func TestBurstIsCoalesced(t *testing.T) {
	root := real(t, t.TempDir())
	w := newW(t)
	w.Add(root, false, nil)
	f := filepath.Join(root, "f")
	for i := 0; i < 20; i++ {
		write(t, f, strings.Repeat("x", i))
	}
	paths, batches := collect(w, 10*debounce)
	if batches != 1 || !slices.Contains(paths, f) {
		t.Fatalf("batches=%d paths=%v", batches, paths)
	}
}

func TestFileRootIgnoresSiblings(t *testing.T) {
	dir := real(t, t.TempDir())
	target := filepath.Join(dir, "CLAUDE.md")
	write(t, target, "v1")
	w := newW(t)
	if err := w.Add(target, true, nil); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "other.md"), "noise")
	write(t, target, "v2")
	paths, _ := collect(w, 10*debounce)
	for _, p := range paths {
		if p != target {
			t.Fatalf("sibling reported: %s", p)
		}
	}
	if !slices.Contains(paths, target) {
		t.Fatal("target change not reported")
	}
}

func TestFileRootSurvivesRenameReplace(t *testing.T) {
	// Editors and Enigma itself save by renaming a temp file over the target.
	dir := real(t, t.TempDir())
	target := filepath.Join(dir, "CLAUDE.md")
	write(t, target, "v1")
	w := newW(t)
	w.Add(target, true, nil)
	for i := 0; i < 3; i++ {
		tmp := filepath.Join(dir, ".tmp-save")
		write(t, tmp, strings.Repeat("y", i+1))
		os.Rename(tmp, target)
		waitFor(t, w, target)
	}
}

func TestTempFilesAreNotReported(t *testing.T) {
	root := real(t, t.TempDir())
	w := newW(t)
	w.Add(root, false, nil)
	write(t, filepath.Join(root, ".enigma-tmp-123"), "x")
	paths, _ := collect(w, 8*debounce)
	if len(paths) != 0 {
		t.Fatalf("reported %v", paths)
	}
}

func TestSkippedDirectoriesAreNotWatched(t *testing.T) {
	root := real(t, t.TempDir())
	os.MkdirAll(filepath.Join(root, "node_modules", "x"), 0o755)
	w := newW(t)
	skip := func(abs string) bool { return filepath.Base(abs) == "node_modules" }
	w.Add(root, false, skip)
	write(t, filepath.Join(root, "node_modules", "x", "i.js"), "x")
	paths, _ := collect(w, 8*debounce)
	for _, p := range paths {
		if strings.Contains(p, "i.js") {
			t.Fatalf("change inside skipped dir reported: %v", paths)
		}
	}
}

func TestRemoveStopsEvents(t *testing.T) {
	root := real(t, t.TempDir())
	w := newW(t)
	w.Add(root, false, nil)
	w.Remove(root)
	write(t, filepath.Join(root, "f"), "x")
	paths, _ := collect(w, 8*debounce)
	if len(paths) != 0 {
		t.Fatalf("reported after Remove: %v", paths)
	}
}

func TestAddCreatesMissingRoot(t *testing.T) {
	root := filepath.Join(real(t, t.TempDir()), "not", "yet")
	w := newW(t)
	if err := w.Add(root, false, nil); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(root, "f")
	write(t, f, "x")
	waitFor(t, w, f)
}
