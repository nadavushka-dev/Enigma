package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

func differingDirPreview(_, _ string) (engine.AdoptionReport, error) {
	local := []proto.ProbeFile{{Path: "a.md", Hash: "a"}, {Path: "b.md", Hash: "b"}}
	peer := []proto.ProbeFile{{Path: "a.md", Hash: "a2"}, {Path: "b.md", Hash: "b2"}}
	return engine.AdoptionReport{
		Path: "notes", Dir: true, Group: proto.MainGroup, GroupName: "main",
		Local: engine.ProbeMachine{Name: "mac", Local: true, Connected: true, Exists: true, Dir: true, Files: local},
		Machines: []engine.ProbeMachine{
			{Name: "mac", Local: true, Connected: true, Exists: true, Dir: true, Files: local},
			{Name: "ubuntu2", Connected: true, Exists: true, Dir: true, Files: peer},
		},
		Files: []engine.AdoptionFile{
			{Path: "a.md", State: engine.AdoptionDifferent, Machines: []string{"ubuntu2"}},
			{Path: "b.md", State: engine.AdoptionDifferent, Machines: []string{"ubuntu2"}},
		},
		Summary: engine.AdoptionSummary{Files: 2, Different: 2},
	}, nil
}

func differingFilePreview(_, _ string) (engine.AdoptionReport, error) {
	local := []proto.ProbeFile{{Path: "", Hash: "a", Size: 4}}
	peer := []proto.ProbeFile{{Path: "", Hash: "b", Size: 5}}
	return engine.AdoptionReport{
		Path: "CLAUDE.md", Group: proto.MainGroup, GroupName: "main",
		Local: engine.ProbeMachine{Name: "mac", Local: true, Connected: true, Exists: true, Files: local},
		Machines: []engine.ProbeMachine{
			{Name: "mac", Local: true, Connected: true, Exists: true, Files: local},
			{Name: "ubuntu2", Connected: true, Exists: true, Files: peer},
		},
		Files:   []engine.AdoptionFile{{Path: "", State: engine.AdoptionDifferent, Machines: []string{"ubuntu2"}}},
		Summary: engine.AdoptionSummary{Files: 1, Different: 1},
	}, nil
}

func TestAdoptFolderDefaultsToKeepBothForEveryFile(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/a.md", "a")
	f.write(t, "notes/b.md", "b")
	f.previewFn = differingDirPreview
	m := filesModel(t, f)
	selected(t, m, "notes")

	drive(t, m, key("a"))
	viewHas(t, m, "What should happen?", "Keep both for every file", "This machine wins for every file", "Choose per file", "Cancel")
	drive(t, m, key("enter")) // the default, keep both for all

	if !reflect.DeepEqual(f.policyCalls, []string{string(proto.FirstAddKeepBoth)}) {
		t.Fatalf("policies %v", f.policyCalls)
	}
	if f.policyPaths[0] != nil {
		t.Fatalf("a global choice needs no per-file map: %v", f.policyPaths[0])
	}
	if c := f.Calls(); len(c) != 1 || !strings.HasSuffix(c[0], "/notes") {
		t.Fatalf("calls %v", c)
	}
}

func TestAdoptFolderGlobalMineWins(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/a.md", "a")
	f.write(t, "notes/b.md", "b")
	f.previewFn = differingDirPreview
	m := filesModel(t, f)
	drive(t, m, key("a"))
	drive(t, m, key("j"), key("enter")) // This machine wins for every file

	if !reflect.DeepEqual(f.policyCalls, []string{string(proto.FirstAddReplace)}) {
		t.Fatalf("policies %v", f.policyCalls)
	}
	if f.policyPaths[0] != nil {
		t.Fatalf("a global choice needs no per-file map: %v", f.policyPaths[0])
	}
}

func TestAdoptFolderPerFileFromTheList(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/a.md", "a")
	f.write(t, "notes/b.md", "b")
	f.previewFn = differingDirPreview
	m := filesModel(t, f)
	drive(t, m, key("a"))
	drive(t, m, key("j"), key("j"), key("enter")) // Choose per file...

	viewHas(t, m, "Choose for each file", "~/notes/a.md", "~/notes/b.md", "keep both", "A apply")

	drive(t, m, key("enter")) // choose a.md
	viewHas(t, m, "~/notes/a.md", "Keep both (recommended)", "This machine wins", "Back")
	drive(t, m, key("j"), key("enter")) // a.md -> mine wins
	viewHas(t, m, "mine wins")
	drive(t, m, key("A")) // apply

	if !reflect.DeepEqual(f.policyCalls, []string{string(proto.FirstAddKeepBoth)}) {
		t.Fatalf("the folder default must stay keep-both for unseen files: %v", f.policyCalls)
	}
	want := map[string]proto.FirstAddPolicy{"a.md": proto.FirstAddReplace}
	if !reflect.DeepEqual(f.policyPaths[0], want) {
		t.Fatalf("per-file choices %v, want %v", f.policyPaths[0], want)
	}
	if m.modal != nil {
		t.Fatal("the dialog stayed open")
	}
}

func TestAdoptFolderPerFileAllMine(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/a.md", "a")
	f.write(t, "notes/b.md", "b")
	f.previewFn = differingDirPreview
	m := filesModel(t, f)
	drive(t, m, key("a"))
	drive(t, m, key("j"), key("j"), key("enter")) // Choose per file...
	drive(t, m, key("M"), key("A"))

	want := map[string]proto.FirstAddPolicy{"a.md": proto.FirstAddReplace, "b.md": proto.FirstAddReplace}
	if !reflect.DeepEqual(f.policyPaths[0], want) {
		t.Fatalf("per-file choices %v, want %v", f.policyPaths[0], want)
	}
}

func TestAdoptEscapeCancels(t *testing.T) {
	f := newFake(t)
	f.write(t, "notes/a.md", "a")
	f.previewFn = differingDirPreview
	m := filesModel(t, f)
	drive(t, m, key("a"))
	drive(t, m, key("esc"))
	if m.modal != nil {
		t.Fatal("the dialog stayed open")
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("cancelled, but added: %v", f.Calls())
	}
	viewHas(t, m, "cancelled; ~/notes was not added")
}

func TestAdoptSingleFileIsAMenu(t *testing.T) {
	f := newFake(t)
	f.write(t, "CLAUDE.md", "a")
	f.previewFn = differingFilePreview
	m := filesModel(t, f)
	selected(t, m, "CLAUDE.md")

	drive(t, m, key("a"))
	viewHas(t, m, "What should happen?", "Keep both (recommended)", "This machine wins", "Cancel")
	drive(t, m, key("j"), key("enter")) // This machine wins

	if !reflect.DeepEqual(f.policyCalls, []string{string(proto.FirstAddReplace)}) {
		t.Fatalf("policies %v", f.policyCalls)
	}
	if f.policyPaths[0] != nil {
		t.Fatalf("a single file needs no per-file map: %v", f.policyPaths[0])
	}
}

func TestAdoptSingleFileKeepBoth(t *testing.T) {
	f := newFake(t)
	f.write(t, "CLAUDE.md", "a")
	f.previewFn = differingFilePreview
	m := filesModel(t, f)
	drive(t, m, key("a"), key("enter"))

	if !reflect.DeepEqual(f.policyCalls, []string{string(proto.FirstAddKeepBoth)}) {
		t.Fatalf("policies %v", f.policyCalls)
	}
}
