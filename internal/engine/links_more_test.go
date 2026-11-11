package engine

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// A link to its own entry root, or to any folder that contains it, is a
// loop for whatever follows it (an agent harness): skipped and reported.
func TestD23_LinkToAnAncestorIsALoop(t *testing.T) {
	a, b := skillsFleet(t)
	a.write(".agents/skills/sub/SKILL.md", "sub")
	a.symlink(".agents/skills/root", a.path(".agents/skills")) // the entry root
	a.symlink(".agents/skills/dot", ".")                       // the same, relative
	a.symlink(".agents/skills/sub/back", "..")                 // an ancestor inside the entry
	a.symlink(".agents/skills/self", "self")                   // itself
	a.e.Rescan()
	for _, rel := range []string{".agents/skills/root", ".agents/skills/dot", ".agents/skills/sub/back"} {
		eventually(t, rel+" skipped", linkState(a, rel, LinkSkipped))
		if l, _ := linkStatus(a, rel); !strings.Contains(l.Reason, "loop") {
			t.Fatalf("%s reason = %q", rel, l.Reason)
		}
	}
	eventually(t, "self skipped", linkState(a, ".agents/skills/self", LinkSkipped))
	eventually(t, "the sibling file still syncs", hasFile(b, ".agents/skills/sub/SKILL.md", "sub"))
	settle(t, a, b)
	for _, rel := range []string{".agents/skills/root", ".agents/skills/dot", ".agents/skills/sub/back", ".agents/skills/self"} {
		if b.readlink(rel) != "<no link>" || cur(b, rel) != nil {
			t.Fatalf("%s was replicated", rel)
		}
	}
}

// Chains are never resolved, and a target behind a symlinked folder of
// its own entry is not what that entry syncs: skipped.
func TestD23_ChainThroughALinkedFolderIsSkipped(t *testing.T) {
	a, b := skillsFleet(t)
	a.write("elsewhere/mid/foo/SKILL.md", "behind")
	a.symlink("repo/skills/mid", a.path("elsewhere/mid")) // a link inside the target entry
	a.symlink(".agents/skills/chain", a.path("repo/skills/mid/foo"))
	a.e.Rescan()
	eventually(t, "skipped", linkState(a, ".agents/skills/chain", LinkSkipped))
	if l, _ := linkStatus(a, ".agents/skills/chain"); !strings.Contains(l.Reason, "behind another link") {
		t.Fatalf("reason = %q", l.Reason)
	}
	settle(t, a, b)
	if b.readlink(".agents/skills/chain") != "<no link>" || cur(b, ".agents/skills/chain") != nil {
		t.Fatal("a link through a linked folder was replicated")
	}
	if b.read("repo/skills/mid/foo/SKILL.md") != "<missing>" {
		t.Fatal("content behind the middle link was synced")
	}
}

// Finding 3: the 2 s link poll lists only what the entry syncs: never a
// folder the entry ignores.
func TestD23_LinkPollSkipsIgnoredFolders(t *testing.T) {
	a := newMachine(t, "a", func(o *Options) { o.NoWatch = true })
	a.write("repo/skills/foo/SKILL.md", "foo")
	a.write(".agents/skills/big/deep/x", "x")
	a.symlink(".agents/skills/big/deep/l", a.path("repo/skills/foo"))
	a.symlink(".agents/skills/node_modules/l", a.path("repo/skills/foo"))
	a.symlink(".agents/skills/foo", a.path("repo/skills/foo"))
	a.add("repo/skills")
	if err := a.e.AddEntry(a.path(".agents/skills"), AddOptions{Links: true, Ignore: []string{"big"}}); err != nil {
		t.Fatal(err)
	}
	var got []string
	a.e.do(func() { got = a.e.entryLinks(".agents/skills") })
	sort.Strings(got)
	if len(got) != 1 || got[0] != ".agents/skills/foo" {
		t.Fatalf("entryLinks = %v, want only the link the entry syncs", got)
	}
	a.e.do(a.e.pollLinks)
	if cur(a, ".agents/skills/foo") == nil || cur(a, ".agents/skills/big/deep/l") != nil {
		t.Fatal("the poll tracked the wrong links")
	}
	if _, err := os.Lstat(a.path(".agents/skills/big/deep/l")); err != nil {
		t.Fatal(err)
	}
}
