package engine

import (
	"testing"

	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/version"
)

func (m *machine) inHistory(content string) bool {
	_, ok := m.e.hist.Get(fsx.Hash([]byte(content)))
	return ok
}

func (m *machine) ancestry(rel string) []string {
	var out []string
	m.e.do(func() {
		fs := m.e.st.Files[rel]
		if fs == nil {
			return
		}
		for _, n := range version.Walk(fs.Anc, fs.V.Hash, version.AncestryLimit) {
			out = append(out, n.Hash)
		}
	})
	return out
}

func TestHistoryKeepsEveryVersionSeen(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("f.md", "v1")
	a.add("f.md")
	link(t, a, b)
	eventually(t, "v1 on b", hasFile(b, "f.md", "v1"))
	a.write("f.md", "v2")
	eventually(t, "v2 on b", hasFile(b, "f.md", "v2"))
	for _, m := range []*machine{a, b} {
		for _, c := range []string{"v1", "v2"} {
			if !m.inHistory(c) {
				t.Errorf("%s: %q not in history", m.name, c)
			}
		}
	}
}

func TestPeerLearnsAncestryItNeverSaw(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("f.md", "v1")
	a.add("f.md")
	cut := link(t, a, b)
	eventually(t, "v1 on b", hasFile(b, "f.md", "v1"))
	cut()
	a.write("f.md", "v2")
	a.e.Rescan()
	a.write("f.md", "v3")
	a.e.Rescan()
	link(t, a, b)
	eventually(t, "v3 on b", hasFile(b, "f.md", "v3"))
	got := b.ancestry("f.md")
	want := []string{fsx.Hash([]byte("v3")), fsx.Hash([]byte("v2")), fsx.Hash([]byte("v1"))}
	if len(got) < 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("b's ancestry %v, want prefix %v", got, want)
	}
}

func TestAncestrySurvivesRestart(t *testing.T) {
	a := newMachine(t, "a")
	a.write("f.md", "v1")
	a.add("f.md")
	a.write("f.md", "v2")
	a.e.Rescan()
	a.restart()
	if got := a.ancestry("f.md"); len(got) != 2 {
		t.Fatalf("ancestry after restart: %v", got)
	}
}
