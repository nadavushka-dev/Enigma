package engine

import (
	"testing"

	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

func addWithPolicy(t *testing.T, m *machine, rel string, policy proto.FirstAddPolicy, byPath map[string]proto.FirstAddPolicy) {
	t.Helper()
	if err := m.e.AddWithPolicy(m.path(rel), nil, "", policy, byPath); err != nil {
		t.Fatal(err)
	}
}

func oneCopy(m *machine, dir, stem string) string {
	c := conflictCopies(m, dir, stem)
	if len(c) != 1 {
		return ""
	}
	for _, v := range c {
		return v
	}
	return ""
}

func TestD22_KeepBothFirstAddKeepsBothCopies(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("f.md", "adder")
	b.write("f.md", "mine")
	link(t, a, b)
	addWithPolicy(t, a, "f.md", proto.FirstAddKeepBoth, nil)

	eventually(t, "adder owns the path on b", hasFile(b, "f.md", "adder"))
	eventually(t, "b keeps its copy", func() bool { return oneCopy(b, ".", "f") == "mine" })
	eventually(t, "b's copy reaches a", func() bool { return oneCopy(a, ".", "f") == "mine" })
	if !backedUp(b, "mine") {
		t.Fatal("the losing copy must also be in b's local backups")
	}
	if backedUp(a, "adder") {
		t.Fatal("the winning copy must not be backed up")
	}
	kept := false
	for _, ev := range a.e.Events(0) {
		if ev.Kind == EvKeptBoth && ev.Detail == "f.md" {
			kept = true
		}
		if ev.Kind == EvConflict && ev.Detail == "f.md" {
			t.Fatal("a first add must not be reported as an ordinary conflict")
		}
	}
	if !kept {
		t.Fatal("the first add did not record a kept-both event")
	}
}

func TestD22_ReplaceFirstAddKeepsOnlyTheBackup(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("f.md", "adder")
	b.write("f.md", "mine")
	link(t, a, b)
	addWithPolicy(t, a, "f.md", proto.FirstAddReplace, nil)

	eventually(t, "adder owns the path on b", hasFile(b, "f.md", "adder"))
	if got := oneCopy(b, ".", "f"); got != "" {
		t.Fatalf("replace must not leave a conflict copy, got %q", got)
	}
	if got := oneCopy(a, ".", "f"); got != "" {
		t.Fatalf("replace must not leave a conflict copy on the adder, got %q", got)
	}
	if !backedUp(b, "mine") {
		t.Fatal("replace must back up the losing copy")
	}
}

func TestD22_FolderFirstAddPerFilePolicies(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("d/keep.md", "a keep")
	b.write("d/keep.md", "b keep")
	a.write("d/replace.md", "a replace")
	b.write("d/replace.md", "b replace")
	link(t, a, b)
	addWithPolicy(t, a, "d", proto.FirstAddKeepBoth, map[string]proto.FirstAddPolicy{
		"replace.md": proto.FirstAddReplace,
	})

	eventually(t, "keep: adder wins at the path", hasFile(b, "d/keep.md", "a keep"))
	eventually(t, "keep: b's copy is on b", func() bool { return oneCopy(b, "d", "keep") == "b keep" })
	eventually(t, "keep: b's copy reaches a", func() bool { return oneCopy(a, "d", "keep") == "b keep" })

	eventually(t, "replace: adder wins at the path", hasFile(b, "d/replace.md", "a replace"))
	if got := oneCopy(b, "d", "replace"); got != "" {
		t.Fatalf("replace must not leave a copy, got %q", got)
	}
	if !backedUp(b, "b replace") {
		t.Fatal("replace must back up b's copy")
	}
}

func TestD22_PreviewReportsMachinesAndDifferences(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("f.md", "adder")
	b.write("f.md", "mine")
	link(t, a, b)
	eventually(t, "a knows b", func() bool { return len(a.e.Machines()) == 2 })

	rep, err := a.e.Preview(a.path("f.md"), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Path != "f.md" || rep.Dir || rep.Group != proto.MainGroup || rep.GroupName != "main" {
		t.Fatalf("report: %+v", rep)
	}
	if !rep.Local.Exists || len(rep.Local.Files) != 1 || rep.Local.Files[0].Hash != fsx.Hash([]byte("adder")) {
		t.Fatalf("local: %+v", rep.Local)
	}
	var peer *ProbeMachine
	for i := range rep.Machines {
		if rep.Machines[i].Name == "b" {
			peer = &rep.Machines[i]
		}
	}
	if peer == nil || !peer.Connected || !peer.Exists || len(peer.Files) != 1 {
		t.Fatalf("peer: %+v", rep.Machines)
	}
	if peer.Files[0].Hash != fsx.Hash([]byte("mine")) {
		t.Fatalf("peer hash: %+v", peer.Files[0])
	}
	if !rep.NeedsDecision() || rep.Summary.Different != 1 {
		t.Fatalf("decision: %+v", rep.Summary)
	}
	if len(rep.Files) != 1 || rep.Files[0].State != AdoptionDifferent || len(rep.Files[0].Machines) != 1 || rep.Files[0].Machines[0] != "b" {
		t.Fatalf("files: %+v", rep.Files)
	}
}

func TestD22_PreviewSameContentNeedsNoDecision(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("f.md", "same")
	b.write("f.md", "same")
	link(t, a, b)
	eventually(t, "a knows b", func() bool { return len(a.e.Machines()) == 2 })

	rep, err := a.e.Preview(a.path("f.md"), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.NeedsDecision() || rep.Summary.Same != 1 || rep.Summary.Different != 0 {
		t.Fatalf("report: %+v", rep)
	}
}

func TestD22_PreviewListsOfflineMachines(t *testing.T) {
	a, b, c := newMachine(t, "a"), newMachine(t, "b"), newMachine(t, "c")
	_ = link(t, a, b)
	cutBC := link(t, b, c)
	cutAC := link(t, a, c)
	for _, m := range []*machine{a, b, c} {
		eventually(t, m.name+" knows everyone", func() bool { return len(m.e.Machines()) == 3 })
	}
	cutBC()
	cutAC()
	eventually(t, "a only sees b", func() bool { return len(a.e.Status().Peers) == 1 })
	a.write("f.md", "adder")

	rep, err := a.e.Preview(a.path("f.md"), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var offline *ProbeMachine
	for i := range rep.Machines {
		if rep.Machines[i].Name == "c" {
			offline = &rep.Machines[i]
		}
	}
	if offline == nil || offline.Connected || offline.Error == "" {
		t.Fatalf("c must be reported offline: %+v", rep.Machines)
	}
	if rep.Summary.Unknown < 1 {
		t.Fatalf("summary: %+v", rep.Summary)
	}
}

func TestD22_PreviewNeverAsksANonMember(t *testing.T) {
	a, _, c, pair := pairFleet(t)
	a.write("p/x.md", "a")
	c.write("p/x.md", "c secret")
	rep, err := a.e.Preview(a.path("p/x.md"), nil, pair)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Machines) != 2 {
		t.Fatalf("only the two members may be listed: %+v", rep.Machines)
	}
	for _, m := range rep.Machines {
		if m.Name == "c" {
			t.Fatalf("a non-member leaked into the report: %+v", rep.Machines)
		}
	}
	if rep.NeedsDecision() {
		t.Fatalf("c must not contribute a difference: %+v", rep.Files)
	}
}

func TestD22_ANonMemberCannotPreviewAGroup(t *testing.T) {
	_, _, c, pair := pairFleet(t)
	c.write("p/x.md", "c")
	if _, err := c.e.Preview(c.path("p/x.md"), nil, pair); err == nil {
		t.Fatal("a non-member must not be able to probe a group")
	}
}
