package fuzzy

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func touch(t *testing.T, home, rel string) {
	t.Helper()
	p := filepath.Join(home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wait(t *testing.T, ix *Index) ([]Entry, Progress) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ents, pr := ix.Snapshot()
		if pr.Done {
			return ents, pr
		}
		if time.Now().After(deadline) {
			t.Fatal("index never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func paths(ents []Entry) []string {
	var out []string
	for _, e := range ents {
		s := e.Path
		if e.Dir {
			s += "/"
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func TestIndexListsNamesUnderHomeAndSkips(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, ".local", "share", "enigma")
	for _, rel := range []string{
		"notes/todo.md", ".zshrc", "proj/.git/HEAD", "proj/node_modules/m.js", "proj/src/main.go",
		"proj/.DS_Store", "proj/a.swp", "Library/Caches/x", ".cache/y", ".Trash/z",
		".local/share/enigma/state.json", ".local/share/other/keep",
	} {
		touch(t, home, rel)
	}
	os.Symlink(filepath.Join(home, "proj"), filepath.Join(home, "link"))
	ix := Build(home, Options{Skip: []string{data}})
	defer ix.Stop()
	ents, pr := wait(t, ix)
	want := []string{
		".local/", ".local/share/", ".local/share/other/", ".local/share/other/keep", ".zshrc",
		"link", "notes/", "notes/todo.md", "proj/", "proj/src/", "proj/src/main.go",
	}
	if got := paths(ents); !reflect.DeepEqual(got, want) {
		t.Fatalf("paths\n got %v\nwant %v", got, want)
	}
	if pr.Count != len(want) || pr.Truncated {
		t.Fatalf("progress %+v", pr)
	}
}

func TestIndexDepthAndCap(t *testing.T) {
	home := t.TempDir()
	touch(t, home, "1/2/3/4/5/6/7/8/9/deep.txt")
	ix := Build(home, Options{})
	ents, _ := wait(t, ix)
	got := paths(ents)
	if got[len(got)-1] != "1/2/3/4/5/6/7/8/" || len(got) != 8 {
		t.Fatalf("depth 8: %v", got)
	}
	for i := 0; i < 20; i++ {
		touch(t, home, "many/"+strings.Repeat("f", i+1))
	}
	ix = Build(home, Options{MaxPaths: 10})
	ents, pr := wait(t, ix)
	if len(ents) != 10 || !pr.Truncated || pr.Count != 10 {
		t.Fatalf("cap: %d entries, progress %+v", len(ents), pr)
	}
}

func TestIndexSnapshotIsSafeWhileBuilding(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 50; i++ {
		touch(t, home, filepath.Join("d", strings.Repeat("a", i+1), "f"))
	}
	ix := Build(home, Options{})
	seen := 0
	for {
		ents, pr := ix.Snapshot()
		if len(ents) != pr.Count {
			t.Fatalf("entries %d, count %d", len(ents), pr.Count)
		}
		seen = max(seen, pr.Count)
		if pr.Done {
			break
		}
	}
	if seen != 101 {
		t.Fatalf("saw %d", seen)
	}
}

func TestIndexStopEndsTheWalk(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 200; i++ {
		touch(t, home, filepath.Join("d", strings.Repeat("a", i+1), "f"))
	}
	ix := Build(home, Options{})
	ix.Stop()
	_, pr := wait(t, ix)
	if !pr.Done {
		t.Fatal("not done after Stop")
	}
}

func TestIndexMissingHomeIsDoneAndEmpty(t *testing.T) {
	ix := Build(filepath.Join(t.TempDir(), "nope"), Options{})
	ents, pr := wait(t, ix)
	if len(ents) != 0 || pr.Count != 0 {
		t.Fatalf("%v %+v", ents, pr)
	}
}
