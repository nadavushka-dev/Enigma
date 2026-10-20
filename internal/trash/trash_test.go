package trash

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
)

type env struct {
	t         *testing.T
	home, dir string
	now       time.Time
}

func newEnv(t *testing.T) *env {
	base := t.TempDir()
	e := &env{t: t, home: filepath.Join(base, "home"), dir: filepath.Join(base, "trash"),
		now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)}
	os.MkdirAll(e.home, 0o755)
	return e
}

func (e *env) file(rel, content string) string {
	p := filepath.Join(e.home, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(content), 0o644)
	return p
}

func (e *env) trash(rel, content string) Item {
	e.t.Helper()
	p := e.file(rel, content)
	it, err := Move(e.dir, p, rel, fsx.Hash([]byte(content)), e.now)
	if err != nil {
		e.t.Fatal(err)
	}
	return it
}

func TestMoveListRestore(t *testing.T) {
	e := newEnv(t)
	it := e.trash(".claude/CLAUDE.md", "# rules")
	if it.ID != "2026-10-20/.claude/CLAUDE.md" || it.Path != ".claude/CLAUDE.md" {
		t.Fatalf("item %+v", it)
	}
	if _, err := os.Stat(filepath.Join(e.home, ".claude/CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatal("file not moved")
	}
	items, err := List(e.dir)
	if err != nil || len(items) != 1 || items[0].ID != it.ID || items[0].Size != 7 {
		t.Fatalf("list %+v %v", items, err)
	}
	got, err := Restore(e.dir, e.home, it.ID)
	if err != nil || got != filepath.Join(e.home, ".claude/CLAUDE.md") {
		t.Fatalf("restore %q %v", got, err)
	}
	if b, _ := os.ReadFile(got); string(b) != "# rules" {
		t.Fatalf("restored %q", b)
	}
	if items, _ := List(e.dir); len(items) != 0 {
		t.Fatalf("still in trash: %+v", items)
	}
}

func TestSamePathTwiceInADayKeepsBothUnambiguously(t *testing.T) {
	e := newEnv(t)
	first := e.trash("notes.md", "first")
	second := e.trash("notes.md", "second")
	if first.ID == second.ID || second.Path != "notes.md" || second.ID != "2026-10-20.2/notes.md" {
		t.Fatalf("first %+v second %+v", first, second)
	}
	// A file really named like a suffixed copy keeps its own name.
	odd := e.trash("notes.2.md", "odd")
	if odd.Path != "notes.2.md" {
		t.Fatalf("odd %+v", odd)
	}
}

func TestFileThenFolderOfTheSameName(t *testing.T) {
	e := newEnv(t)
	e.trash("a", "file a")
	it := e.trash("a/b", "inside folder a")
	if it.Path != "a/b" {
		t.Fatalf("item %+v", it)
	}
}

func TestMoveRefusesChangedFile(t *testing.T) {
	e := newEnv(t)
	p := e.file("f", "now")
	if _, err := Move(e.dir, p, "f", fsx.Hash([]byte("then")), e.now); !errors.Is(err, fsx.ErrChanged) {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreNeverOverwrites(t *testing.T) {
	e := newEnv(t)
	it := e.trash("f.md", "old")
	e.file("f.md", "new")
	if _, err := Restore(e.dir, e.home, it.ID); err == nil {
		t.Fatal("restore overwrote an existing file")
	}
	if b, _ := os.ReadFile(filepath.Join(e.home, "f.md")); string(b) != "new" {
		t.Fatalf("got %q", b)
	}
}

func TestRestoreRejectsBadIDs(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"", "2026-10-20", "../x", "2026-10-20/../../x", "/etc/passwd", "2026-10-20/missing"} {
		if _, err := Restore(e.dir, e.home, id); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
}

func TestPurgeDropsOlderThanMaxAge(t *testing.T) {
	e := newEnv(t)
	old := e.now.AddDate(0, 0, -31)
	p := e.file("old.md", "old")
	Move(e.dir, p, "old.md", fsx.Hash([]byte("old")), old)
	keep := e.trash("new.md", "new")
	Purge(e.dir, 30*24*time.Hour, e.now)
	items, _ := List(e.dir)
	if len(items) != 1 || items[0].ID != keep.ID {
		t.Fatalf("after purge %+v", items)
	}
}

func TestEmpty(t *testing.T) {
	e := newEnv(t)
	e.trash("a", "1")
	e.trash("b/c", "2")
	if n, err := Empty(e.dir); err != nil || n != 2 {
		t.Fatalf("empty %d %v", n, err)
	}
	if items, _ := List(e.dir); len(items) != 0 {
		t.Fatalf("left %+v", items)
	}
}

func TestListOfMissingTrashIsEmpty(t *testing.T) {
	if items, err := List(filepath.Join(t.TempDir(), "none")); err != nil || len(items) != 0 {
		t.Fatalf("%+v %v", items, err)
	}
}
