package tui

import (
	"path/filepath"
	"testing"

	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

// D21 parity for `enigma entry merge`: M on a synced entry asks for the
// merge, and the entry's mark shows it.
func TestFilesSetsMerge(t *testing.T) {
	f := newFake(t)
	f.write(t, "auth.json", "{}")
	f.write(t, "notes.md", "x")
	f.status.Entries = []engine.EntryStatus{{Path: "auth.json"}}
	m := filesModel(t, f)
	drive(t, m, m.fetchStatus()())
	for files(m).current().rel != "auth.json" {
		drive(t, m, key("j"))
	}
	drive(t, m, key("M"))
	viewHas(t, m, "Merge for ~/auth.json", "lines")
	for range "lines" {
		drive(t, m, key("backspace"))
	}
	typeText(t, m, "json expires")
	drive(t, m, key("enter"))
	want := "merge json expires " + filepath.Join(f.home, "auth.json")
	if c := f.Calls(); len(c) != 1 || c[0] != want {
		t.Fatalf("calls %v, want [%s]", c, want)
	}
	viewHas(t, m, "~/auth.json merges by JSON key")

	f.status.Entries[0].Merge, f.status.Entries[0].MergeNewestBy = proto.MergeJSON, "expires"
	drive(t, m, m.fetchStatus()())
	if got := files(m).markRel(m.s, "auth.json", false); got != "synced · json merge" {
		t.Fatalf("mark %q", got)
	}
	drive(t, m, key("M"))
	viewHas(t, m, "json expires") // the prompt starts at the current setting
	drive(t, m, key("esc"))

	// a bad answer changes nothing
	drive(t, m, key("M"))
	typeText(t, m, " x y")
	drive(t, m, key("enter"))
	viewHas(t, m, "lines, json, or json FIELD")
	if len(f.Calls()) != 1 {
		t.Fatalf("calls %v", f.Calls())
	}

	// not synced
	for files(m).current().rel != "notes.md" {
		drive(t, m, key("j"))
	}
	drive(t, m, key("M"))
	viewHas(t, m, "M sets how a synced file or folder merges")
}
