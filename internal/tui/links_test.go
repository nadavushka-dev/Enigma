package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nadavushka-dev/enigma/internal/engine"
)

// D21 parity for D23: s toggles link replication on a synced folder, and
// links show their state.
func TestD23_FilesTogglesLinksAndShowsThem(t *testing.T) {
	f := newFake(t)
	f.write(t, "repo/foo/SKILL.md", "x")
	f.write(t, "skills/plain.md", "x")
	if err := os.Symlink(filepath.Join(f.home, "repo", "foo"), filepath.Join(f.home, "skills", "foo")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(f.home, "skills", "out")); err != nil {
		t.Fatal(err)
	}
	f.status.Entries = []engine.EntryStatus{
		{Path: "repo", Dir: true},
		{Path: "skills", Dir: true},
	}
	m := filesModel(t, f)
	drive(t, m, m.fetchStatus()())
	for files(m).current().rel != "skills" {
		drive(t, m, key("j"))
	}
	drive(t, m, key("s"))
	want := "links on " + filepath.Join(f.home, "skills")
	if c := f.Calls(); len(c) != 1 || c[0] != want {
		t.Fatalf("calls %v, want [%s]", c, want)
	}
	viewHas(t, m, "links are replicated in ~/skills")

	f.status.Entries[1].LinksOn = true
	f.status.Entries[1].Links = []engine.LinkStatus{
		{Path: "skills/foo", Target: "~/repo/foo", State: engine.LinkSynced},
		{Path: "skills/out", Target: "/etc", State: engine.LinkSkipped, Reason: "target outside your home folder"},
	}
	drive(t, m, m.fetchStatus()())
	p := files(m)
	if got := p.markRel(m.s, "skills", true); got != "synced · links" {
		t.Fatalf("entry mark %q", got)
	}
	drive(t, m, key("l"))
	marks := map[string]string{}
	dim := map[string]bool{}
	for _, r := range p.rows {
		marks[r.rel], dim[r.rel] = p.mark(m.s, r), p.dimmed(m.s, r)
	}
	if marks["skills/foo"] != "link synced" || dim["skills/foo"] {
		t.Fatalf("synced link: mark %q, dimmed %v", marks["skills/foo"], dim["skills/foo"])
	}
	if marks["skills/out"] != "link skipped" || !dim["skills/out"] {
		t.Fatalf("skipped link: mark %q, dimmed %v", marks["skills/out"], dim["skills/out"])
	}
	viewHas(t, m, "foo -> ~/repo/foo")

	// off again from the folder
	for p.current().rel != "skills" {
		drive(t, m, key("k"))
	}
	drive(t, m, key("s"))
	if c := f.Calls(); len(c) != 2 || c[1] != "links off "+filepath.Join(f.home, "skills") {
		t.Fatalf("calls %v", c)
	}
	// not a synced folder
	drive(t, m, key("j"))
	drive(t, m, key("s"))
	viewHas(t, m, "s turns link replication on or off for a synced folder")
	if len(f.Calls()) != 2 {
		t.Fatalf("calls %v", f.Calls())
	}
}
