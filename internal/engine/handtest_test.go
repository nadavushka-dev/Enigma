package engine

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Hand test S1: a skipped link's note goes when the link goes, without a
// restart.
func TestD23_SkippedLinkNoteClearsWhenTheLinkGoes(t *testing.T) {
	a, _ := skillsFleet(t)
	a.symlink(".agents/skills/etc", "/etc")
	eventually(t, "skipped", linkState(a, ".agents/skills/etc", LinkSkipped))
	os.Remove(a.path(".agents/skills/etc")) // file events and polls only: no rescan
	eventually(t, "no longer reported", func() bool {
		_, ok := linkStatus(a, ".agents/skills/etc")
		return !ok && !skippedHas(a, ".agents/skills/etc")
	})
}

// Hand test S1: a root that vanished and came back leaves no standing
// error behind (the missing-locally problem comes from the status).
func TestD15_MissingRootLeavesNoErrorEvents(t *testing.T) {
	a, b := notesFleet(t, noWatch)
	os.Rename(a.path("notes"), a.path("notes.old"))
	a.e.Rescan()
	os.Rename(a.path("notes.old"), a.path("notes"))
	a.e.Rescan()
	settle(t, a, b)
	for _, ev := range a.e.Events(0) {
		if ev.Kind == EvError {
			t.Fatalf("an error event stands for a root that is back: %+v", ev)
		}
	}
	if a.e.Status().Entries[0].Missing {
		t.Fatal("still missing")
	}
}

// Hand test S3: a file under a link here (scenario 4), deleted on the
// machine that had the real folder, is no longer listed as skipped.
func TestD15_DeletedShadowedFileIsNotSkippedForever(t *testing.T) {
	a, b := skillsFleet(t)
	eventually(t, "synced", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	cutAll(t, a, b)
	a.symlink(".agents/skills/bar", a.path("repo/skills/foo")) // during the cut, as in the hand test
	b.write(".agents/skills/bar/SKILL.md", "b's own")
	a.e.Rescan()
	b.e.Rescan()
	link(t, a, b)
	eventually(t, "a lists the file under its link as skipped", func() bool { return skippedHas(a, ".agents/skills/bar/SKILL.md") })
	os.RemoveAll(b.path(".agents/skills/bar")) // b follows the hint; no rescan anywhere
	eventually(t, "no longer skipped on a", func() bool { return !skippedHas(a, ".agents/skills/bar/SKILL.md") })
	if a.read("repo/skills/foo/SKILL.md") != "foo" {
		t.Fatal("the delete went through a's link")
	}
}

// Hand test 3, F1: the blocking folder's files are deleted before the
// link lands in its place, whatever runs first: here the link's retry runs
// before any refresh has seen the folder go (b has no watcher). The
// deletes are recorded and spread, nothing stays skipped, and removing the
// link later resurrects nothing.
func TestD23_BlockingFolderDeletesAreRecordedBeforeTheLink(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b", noWatch)
	a.write("repo/skills/foo/SKILL.md", "foo")
	a.write(".agents/skills/x.md", "x")
	a.add("repo/skills")
	a.addLinks(".agents/skills")
	link(t, a, b)
	eventually(t, "b has the entry", hasFile(b, ".agents/skills/x.md", "x"))
	cutAll(t, a, b)
	a.symlink(".agents/skills/bar", a.path("repo/skills/foo"))
	b.write(".agents/skills/bar/SKILL.md", "b real")
	a.e.Rescan()
	b.e.Rescan()
	link(t, a, b)
	eventually(t, "blocked on b", linkState(b, ".agents/skills/bar", LinkBlocked))
	eventually(t, "a has b's file under its link", func() bool { return skippedHas(a, ".agents/skills/bar/SKILL.md") })

	os.RemoveAll(b.path(".agents/skills/bar")) // the hint; no refresh on b
	b.e.do(b.e.retryPending)                   // the link's retry comes first
	if b.readlink(".agents/skills/bar") != b.path("repo/skills/foo") {
		t.Fatalf("the link did not land: %q", b.readlink(".agents/skills/bar"))
	}
	if v := cur(b, ".agents/skills/bar/SKILL.md"); v == nil || !v.IsDeleted() {
		t.Fatalf("b's delete of its folder's file was not recorded: %+v", v)
	}
	eventually(t, "not skipped on a", func() bool { return !skippedHas(a, ".agents/skills/bar/SKILL.md") })
	settle(t, a, b)
	if skippedHas(b, ".agents/skills/bar/SKILL.md") {
		t.Fatal("skipped on b")
	}
	os.Remove(a.path(".agents/skills/bar")) // and later the link goes
	a.e.Rescan()
	settle(t, a, b)
	b.e.Rescan()
	settle(t, a, b)
	for _, m := range []*machine{a, b} {
		if got := m.read(".agents/skills/bar/SKILL.md"); got != "<missing>" {
			t.Fatalf("%s: the deleted file came back: %q", m.name, got)
		}
	}
}

// Hand test S2: following the "blocked" hint (deleting the folder) lets
// the link in, without a restart.
func TestD23_BlockedLinkArrivesOnceTheFolderIsGone(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("repo/skills/foo/SKILL.md", "foo")
	a.write(".agents/skills/x.md", "x")
	a.symlink(".agents/skills/bar", a.path("repo/skills/foo"))
	b.write(".agents/skills/bar/SKILL.md", "b's own")
	a.add("repo/skills")
	a.addLinks(".agents/skills")
	link(t, a, b)
	eventually(t, "blocked on b", linkState(b, ".agents/skills/bar", LinkBlocked))
	os.RemoveAll(b.path(".agents/skills/bar"))
	b.e.Rescan()
	eventually(t, "the link arrives", hasLink(b, ".agents/skills/bar", b.path("repo/skills/foo")))
	eventually(t, "synced", linkState(b, ".agents/skills/bar", LinkSynced))
}

// Hand test S4: the machine whose link lost a concurrent retarget keeps it
// in its trash, like every machine that had it.
func TestD23_LosingLinkAuthorKeepsItInTrash(t *testing.T) {
	a, b, c := newMachine(t, "a"), newMachine(t, "b"), newMachine(t, "c")
	for _, n := range []string{"foo", "bar", "baz"} {
		a.write("repo/skills/"+n+"/SKILL.md", n)
	}
	a.symlink(".agents/skills/s", "../../repo/skills/foo")
	knownFleet(t, a, b, c)
	a.add("repo/skills")
	a.addLinks(".agents/skills")
	for _, m := range []*machine{b, c} {
		eventually(t, m.name+" has every target", hasFile(m, "repo/skills/baz/SKILL.md", "baz"))
		eventually(t, m.name+" has the link", hasLink(m, ".agents/skills/s", "../../repo/skills/foo"))
	}
	settle(t, a, b, c)
	cutAll(t, b)
	a.retarget(".agents/skills/s", "../../repo/skills/bar")
	a.e.Rescan()
	eventually(t, "c follows a", hasLink(c, ".agents/skills/s", "../../repo/skills/bar"))
	time.Sleep(1100 * time.Millisecond) // b's retarget is newer
	b.retarget(".agents/skills/s", "../../repo/skills/baz")
	b.e.Rescan()
	link(t, b, c) // b meets only c: c resolves, and a hears the result from c
	for _, m := range []*machine{a, b, c} {
		eventually(t, m.name+" has b's retarget", hasLink(m, ".agents/skills/s", "../../repo/skills/baz"))
	}
	settle(t, a, b, c)
	if !linkTrashed(a, "../../repo/skills/bar") {
		t.Fatal("the author of the losing link has nothing in its trash")
	}
	lost := false
	for _, ev := range a.e.Events(0) {
		lost = lost || ev.Kind == EvLinkLost
	}
	if !lost {
		t.Fatal("the author of the losing link was not told")
	}
}

// Hand test S5: with links off, links are not synced in either direction:
// deleting one does not delete it on the peers.
func TestD23_LinksOffDeletesDoNotSpread(t *testing.T) {
	a, b := skillsFleet(t)
	eventually(t, "link on b", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	if err := a.e.SetLinks(a.path(".agents/skills"), false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b has links off", func() bool {
		for _, es := range b.e.Status().Entries {
			if es.Path == ".agents/skills" {
				return !es.LinksOn
			}
		}
		return false
	})
	os.Remove(a.path(".agents/skills/foo"))
	a.e.Rescan()
	settle(t, a, b)
	if b.readlink(".agents/skills/foo") != b.path("repo/skills/foo") || linkTrashed(b, b.path("repo/skills/foo")) {
		t.Fatal("a link delete spread with links off")
	}
	// Back on: links sync again, so the link the peers still have comes
	// back here (removing it while off was a local change only).
	if err := a.e.SetLinks(a.path(".agents/skills"), true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the peers' link comes back", hasLink(a, ".agents/skills/foo", a.path("repo/skills/foo")))
	if !strings.HasPrefix(b.readlink(".agents/skills/foo"), b.home) {
		t.Fatal("b's link changed")
	}
}
