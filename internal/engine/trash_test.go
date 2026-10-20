package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nadavushka-dev/enigma/internal/trash"
)

func TestS9_RestoredFileSyncsAsANormalEdit(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("n/f.md", "keep me")
	a.add("n")
	link(t, a, b)
	eventually(t, "synced", hasFile(b, "n/f.md", "keep me"))
	a.write("n/other.md", "x") // keeps the root folder present after the delete
	eventually(t, "other synced", hasFile(b, "n/other.md", "x"))
	if err := os.Remove(a.path("n/f.md")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "deleted on b", hasFile(b, "n/f.md", "<missing>"))

	items, err := trash.List(filepath.Join(b.data, "trash"))
	if err != nil || len(items) != 1 || items[0].Path != "n/f.md" {
		t.Fatalf("b's trash: %+v %v", items, err)
	}
	if _, err := trash.Restore(filepath.Join(b.data, "trash"), b.home, items[0].ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "restore reaches a", hasFile(a, "n/f.md", "keep me"))
}
