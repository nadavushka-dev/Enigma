package fsx

import (
	"os"
	"path/filepath"
	"testing"
)

// A folder deleted and re-created at once may get the same inode back
// (Linux does); its birth time still tells it apart.
func TestDirIDTellsARecreatedFolderApart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "notes")
	os.Mkdir(p, 0o755)
	a, ok := DirIDOf(p)
	if !ok || a.Btime == 0 {
		t.Fatalf("DirIDOf = %+v, %v; want a birth time on this platform", a, ok)
	}
	if again, _ := DirIDOf(p); !SameFolder(a, again) {
		t.Fatal("the same folder is not the same")
	}
	os.RemoveAll(p)
	os.Mkdir(p, 0o755)
	b, _ := DirIDOf(p)
	if SameFolder(a, b) {
		t.Fatalf("a re-created folder is taken for the old one: %+v vs %+v", a, b)
	}
	os.Remove(p)
	os.Symlink(t.TempDir(), p)
	if _, ok := DirIDOf(p); ok {
		t.Fatal("a symlink has a folder identity")
	}
}

func TestSameFolder(t *testing.T) {
	for _, c := range []struct {
		a, b DirID
		want bool
	}{
		{DirID{1, 10, 5}, DirID{1, 10, 5}, true},
		{DirID{1, 10, 5}, DirID{2, 10, 5}, true},  // device drift (btrfs, NFS)
		{DirID{1, 10, 5}, DirID{1, 10, 6}, false}, // inode reused
		{DirID{1, 10, 5}, DirID{1, 11, 5}, false},
		{DirID{1, 10, 0}, DirID{1, 10, 0}, true}, // no birth times: device and inode
		{DirID{1, 10, 0}, DirID{2, 10, 0}, false},
		{DirID{1, 10, 5}, DirID{1, 10, 0}, true},
	} {
		if got := SameFolder(c.a, c.b); got != c.want {
			t.Errorf("SameFolder(%+v, %+v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
