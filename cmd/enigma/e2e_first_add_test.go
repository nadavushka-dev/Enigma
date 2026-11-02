package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nadavushka-dev/enigma/internal/engine"
)

// The bug that started D22: adding a path that already exists on another
// machine used to replace that machine's copy after a hidden backup. The
// preview must say where it exists and the user must choose; keep-both
// leaves a visible copy on both machines that saw the conflict.
func TestE2E_D22_FirstAddPreviewAndKeepBoth(t *testing.T) {
	w := newWorld(t)
	a, b := w.host("a"), w.host("b")
	a.must("init", "--name", "a")
	a.startDaemon()
	a.add(b)
	eventuallyT(t, "b linked", func() bool { return a.connectedTo("b") })

	a.write("test.txt", "from a")
	b.write("test.txt", "from b")

	// No terminal and no --on-existing: fail closed, and change nothing.
	out, code := a.run("add", "~/test.txt")
	if code == 0 || !strings.Contains(out, "--on-existing=keep-both|mine-wins|cancel") {
		t.Fatalf("a difference without a choice must fail:\n%s", out)
	}
	if !strings.Contains(out, "b") || !strings.Contains(out, "different") {
		t.Fatalf("the report must name the machine and the difference:\n%s", out)
	}
	if b.read("test.txt") != "from b" || len(a.status().Entries) != 0 {
		t.Fatalf("nothing may be added: b=%q entries=%v", b.read("test.txt"), a.status().Entries)
	}

	// The machine-readable report is the same data.
	var dry addResult
	if err := json.Unmarshal([]byte(a.must("add", "--dry-run", "--json", "~/test.txt")), &dry); err != nil {
		t.Fatal(err)
	}
	if dry.Action != "dry-run" || dry.Report.Summary.Different != 1 {
		t.Fatalf("dry run: %+v", dry)
	}
	var peer *engine.ProbeMachine
	for i := range dry.Report.Machines {
		if dry.Report.Machines[i].Name == "b" {
			peer = &dry.Report.Machines[i]
		}
	}
	if peer == nil || !peer.Connected || !peer.Exists {
		t.Fatalf("the report must include b: %+v", dry.Report.Machines)
	}

	// Keep both: the adding machine owns the path; b's copy stays visible.
	a.must("add", "--on-existing=keep-both", "~/test.txt")
	eventuallyT(t, "a's version on b", func() bool { return b.read("test.txt") == "from a" })

	var bc []conflictJSON
	if err := json.Unmarshal([]byte(b.must("conflicts", "--local", "--json")), &bc); err != nil || len(bc) != 1 {
		t.Fatalf("b's copies: %v %+v", err, bc)
	}
	if bc[0].Original != "test.txt" || bc[0].Machine != "b" ||
		!strings.HasPrefix(bc[0].Path, "test.conflict-b-") || !strings.HasSuffix(bc[0].Path, ".txt") {
		t.Fatalf("b's copy: %+v", bc[0])
	}
	if b.read(bc[0].Path) != "from b" {
		t.Fatalf("b's copy holds %q", b.read(bc[0].Path))
	}

	// A single-file copy is local to the machines that resolved it: the
	// adder has one too, so the user sees both versions from either side.
	var ac []conflictJSON
	if err := json.Unmarshal([]byte(a.must("conflicts", "--local", "--json")), &ac); err != nil || len(ac) != 1 {
		t.Fatalf("a's copies: %v %+v", err, ac)
	}
	if ac[0].Path != bc[0].Path || a.read(ac[0].Path) != "from b" {
		t.Fatalf("a's copy: %+v holds %q", ac[0], a.read(ac[0].Path))
	}
}

func TestE2E_D22_MineWinsBacksUp(t *testing.T) {
	w := newWorld(t)
	a, b := w.host("a"), w.host("b")
	a.must("init", "--name", "a")
	a.startDaemon()
	a.add(b)
	eventuallyT(t, "b linked", func() bool { return a.connectedTo("b") })

	a.write("mine.txt", "from a")
	b.write("mine.txt", "from b")
	a.must("add", "--on-existing=mine-wins", "~/mine.txt")
	eventuallyT(t, "a's version on b", func() bool { return b.read("mine.txt") == "from a" })
	if out := b.must("conflicts", "--local", "--json"); strings.Contains(out, "mine.txt.conflict-") {
		t.Fatalf("mine-wins must not leave a copy: %s", out)
	}
	found := false
	filepath.Walk(filepath.Join(b.eh, "backups"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if data, _ := os.ReadFile(p); string(data) == "from b" {
				found = true
			}
		}
		return nil
	})
	if !found {
		t.Fatal("mine-wins must keep b's copy in b's local backups")
	}
}
