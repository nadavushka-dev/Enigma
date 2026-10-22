package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeAdder struct{ adds []string }

func (f *fakeAdder) Add(abs string, ignore []string) error {
	f.adds = append(f.adds, abs+" "+strings.Join(ignore, ","))
	return nil
}

// pickWorld fakes the terminal, fzf and the daemon around `enigma add`.
func pickWorld(t *testing.T, terminal, fzf bool, picks []string) (*fakeAdder, *[]string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ENIGMA_HOME", filepath.Join(home, ".enigma"))
	os.MkdirAll(filepath.Join(home, ".enigma"), 0o755)
	os.WriteFile(filepath.Join(home, ".enigma", "state.json"), []byte("{}"), 0o644)
	var fed []string
	fa := &fakeAdder{}
	oldT, oldF, oldR, oldA := pickerIsTerminal, haveFzf, runFzf, entryAdder
	t.Cleanup(func() { pickerIsTerminal, haveFzf, runFzf, entryAdder = oldT, oldF, oldR, oldA })
	pickerIsTerminal = func() bool { return terminal }
	haveFzf = func() bool { return fzf }
	runFzf = func(lines []string) ([]string, error) { fed = lines; return picks, nil }
	entryAdder = func() (adder, error) { return fa, nil }
	return fa, &fed, home
}

func runAdd(args ...string) (string, int) {
	var out bytes.Buffer
	code := run(append([]string{"add"}, args...), &ctx{stdout: &out, stderr: &out})
	return out.String(), code
}

func TestAddWithoutAPathNeedsATerminalAndFzf(t *testing.T) {
	for _, tc := range []struct{ terminal, fzf bool }{{false, true}, {true, false}, {false, false}} {
		fa, fed, _ := pickWorld(t, tc.terminal, tc.fzf, []string{"x"})
		out, code := runAdd()
		if code != 1 || !strings.Contains(out, "give a path, or install fzf to pick one") {
			t.Fatalf("%+v: %d %q", tc, code, out)
		}
		if len(*fed) != 0 || len(fa.adds) != 0 {
			t.Fatalf("%+v: fzf ran or something was added", tc)
		}
	}
}

func TestAddWithoutAPathPipesTheHomeIndexIntoFzfAndAddsEachPick(t *testing.T) {
	fa, fed, home := pickWorld(t, true, true, []string{"notes/", "a.txt"})
	for _, rel := range []string{"notes/todo.md", "a.txt", "proj/.git/HEAD", "proj/x"} {
		p := filepath.Join(home, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	out, code := runAdd("--ignore", "*.log")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	want := []string{"a.txt", "notes/", "notes/todo.md", "proj/", "proj/x"}
	if !reflect.DeepEqual(*fed, want) {
		t.Fatalf("fzf got %v, want %v (the data folder and .git skipped)", *fed, want)
	}
	wantAdds := []string{filepath.Join(home, "notes") + " *.log", filepath.Join(home, "a.txt") + " *.log"}
	if !reflect.DeepEqual(fa.adds, wantAdds) {
		t.Fatalf("adds %v, want %v", fa.adds, wantAdds)
	}
	if !strings.Contains(out, "syncing ~/notes/ on every machine") || !strings.Contains(out, "syncing ~/a.txt on every machine") {
		t.Fatalf("output %q", out)
	}
}

func TestAddWithoutAPathCancelledInFzfAddsNothing(t *testing.T) {
	fa, _, _ := pickWorld(t, true, true, nil)
	out, code := runAdd()
	if code != 0 || len(fa.adds) != 0 || strings.Contains(out, "syncing") {
		t.Fatalf("%d %q adds %v", code, out, fa.adds)
	}
}

func TestAddWithoutAPathReportsAnFzfFailure(t *testing.T) {
	pickWorld(t, true, true, nil)
	runFzf = func([]string) ([]string, error) { return nil, errors.New("fzf: exit status 2") }
	out, code := runAdd()
	if code != 1 || !strings.Contains(out, "fzf: exit status 2") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestAddWithAPathStillAddsIt(t *testing.T) {
	fa, fed, home := pickWorld(t, true, true, []string{"x"})
	os.WriteFile(filepath.Join(home, "f.txt"), []byte("x"), 0o644)
	out, code := runAdd("~/f.txt")
	if code != 0 || len(*fed) != 0 || !reflect.DeepEqual(fa.adds, []string{filepath.Join(home, "f.txt") + " "}) {
		t.Fatalf("%d %q adds %v fed %v", code, out, fa.adds, *fed)
	}
	if _, code := runAdd("a", "b"); code != 2 {
		t.Fatalf("two paths: exit %d", code)
	}
}
