package trash

import (
	"os"
	"path/filepath"
	"testing"
)

// D23: a trashed link is the link itself; restore recreates it, never
// over an existing path, and its target is never touched.
func TestLinkMoveListRestore(t *testing.T) {
	e := newEnv(t)
	target := e.file("repo/foo/SKILL.md", "skill")
	l := filepath.Join(e.home, ".agents", "skills", "foo")
	os.MkdirAll(filepath.Dir(l), 0o755)
	os.Symlink(filepath.Dir(target), l)

	if _, err := MoveLink(e.dir, l, ".agents/skills/foo", "wrong", e.now); err == nil {
		t.Fatal("moved a link whose target changed")
	}
	it, err := MoveLink(e.dir, l, ".agents/skills/foo", filepath.Dir(target), e.now)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "skill" {
		t.Fatal("target touched")
	}
	items, _ := List(e.dir)
	if len(items) != 1 || items[0].ID != it.ID {
		t.Fatalf("List = %+v", items)
	}
	os.WriteFile(l, []byte("in the way"), 0o644)
	if _, err := Restore(e.dir, e.home, it.ID); err == nil {
		t.Fatal("restore overwrote a file")
	}
	os.Remove(l)
	dst, err := Restore(e.dir, e.home, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(dst); got != filepath.Dir(target) {
		t.Fatalf("restored link -> %q", got)
	}
	if items, _ := List(e.dir); len(items) != 0 {
		t.Fatalf("still in the trash: %+v", items)
	}
}
