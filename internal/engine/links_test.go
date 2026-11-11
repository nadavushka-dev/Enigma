package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/proto"
	"github.com/nadavushka-dev/enigma/internal/version"
)

// ---- link replication (D23) harness -------------------------------------------

func (m *machine) symlink(rel, target string) {
	m.t.Helper()
	p := m.path(rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.Symlink(target, p); err != nil {
		m.t.Fatal(err)
	}
}

// retarget replaces the link at rel, as `ln -sfn` does.
func (m *machine) retarget(rel, target string) {
	m.t.Helper()
	tmp := m.path(rel) + ".tmp-retarget"
	if err := os.Symlink(target, tmp); err != nil {
		m.t.Fatal(err)
	}
	if err := os.Rename(tmp, m.path(rel)); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) readlink(rel string) string {
	s, err := os.Readlink(m.path(rel))
	if err != nil {
		return "<no link>"
	}
	return s
}

func (m *machine) addLinks(rel string) {
	m.t.Helper()
	if err := m.e.AddEntry(m.path(rel), AddOptions{Links: true}); err != nil {
		m.t.Fatal(err)
	}
}

// cur is m's current version of rel, read on its loop.
func cur(m *machine, rel string) (v *version.Version) {
	m.e.do(func() { v = m.e.current(rel) })
	return v
}

func hasLink(m *machine, rel, target string) func() bool {
	return func() bool { return m.readlink(rel) == target }
}

func linkStatus(m *machine, rel string) (LinkStatus, bool) {
	for _, es := range m.e.Status().Entries {
		for _, l := range es.Links {
			if l.Path == rel {
				return l, true
			}
		}
	}
	return LinkStatus{}, false
}

func linkState(m *machine, rel, state string) func() bool {
	return func() bool { l, ok := linkStatus(m, rel); return ok && l.State == state }
}

// linkTrashed: a link to target is in m's trash (the link itself).
func linkTrashed(m *machine, target string) bool {
	found := false
	filepath.Walk(filepath.Join(m.data, "trash"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if t, _ := os.Readlink(p); t == target {
				found = true
			}
		}
		return nil
	})
	return found
}

// skillsFleet: a has ~/repo/skills/foo/SKILL.md and ~/.agents/skills/foo
// linked to it (absolute, under a's home); both folders are entries, the
// skills one with links on. b is linked.
func skillsFleet(t *testing.T) (a, b *machine) {
	t.Helper()
	a, b = newMachine(t, "a"), newMachine(t, "b")
	a.write("repo/skills/foo/SKILL.md", "foo")
	a.write("repo/skills/bar/SKILL.md", "bar")
	a.write(".agents/skills/plain/SKILL.md", "plain")
	a.symlink(".agents/skills/foo", a.path("repo/skills/foo"))
	a.add("repo/skills")
	a.addLinks(".agents/skills")
	link(t, a, b)
	return a, b
}

// ---- tests ------------------------------------------------------------------

func TestD23_LinkReplicatedWhenTargetManaged(t *testing.T) {
	a, b := skillsFleet(t)
	eventually(t, "target synced", hasFile(b, "repo/skills/foo/SKILL.md", "foo"))
	// b's own home, not a's: a home-relative target is materialized per machine.
	eventually(t, "link on b", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	if a.readlink(".agents/skills/foo") != a.path("repo/skills/foo") {
		t.Fatal("a's link changed")
	}
	if b.read(".agents/skills/foo/SKILL.md") != "foo" {
		t.Fatal("b's link does not reach the skill")
	}
	eventually(t, "synced on both", func() bool {
		return linkState(a, ".agents/skills/foo", LinkSynced)() && linkState(b, ".agents/skills/foo", LinkSynced)()
	})
	for _, m := range []*machine{a, b} {
		for _, es := range m.e.Status().Entries {
			if contains(es.Skipped, ".agents/skills/foo") {
				t.Fatalf("%s lists the synced link as skipped", m.name)
			}
		}
	}
}

func TestD23_HomeRelativeAcrossDifferentHomesHasNoEcho(t *testing.T) {
	a, b := skillsFleet(t)
	eventually(t, "link on b", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	settle(t, a, b)
	before := map[string]version.Vector{}
	for _, m := range []*machine{a, b} {
		before[m.name] = cur(m, ".agents/skills/foo").Vector
	}
	a.e.Rescan()
	b.e.Rescan()
	settle(t, a, b)
	for _, m := range []*machine{a, b} {
		if got := cur(m, ".agents/skills/foo").Vector; version.Compare(got, before[m.name]) != version.Equal {
			t.Fatalf("%s made a new version of an unchanged link: %v -> %v", m.name, before[m.name], got)
		}
	}
}

func TestD23_RelativeLinkKeptVerbatim(t *testing.T) {
	a, b := skillsFleet(t)
	a.symlink(".agents/skills/bar", "../../repo/skills/bar")
	eventually(t, "relative link on b", hasLink(b, ".agents/skills/bar", "../../repo/skills/bar"))
	eventually(t, "reaches the skill", hasFile(b, ".agents/skills/bar/SKILL.md", "bar"))
}

func TestD23_LinkToUnmanagedTargetSkipped(t *testing.T) {
	a, b := skillsFleet(t)
	a.write("elsewhere/x/SKILL.md", "x")
	a.symlink(".agents/skills/x", a.path("elsewhere/x"))
	eventually(t, "reported skipped", linkState(a, ".agents/skills/x", LinkSkipped))
	eventually(t, "the managed one still syncs", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	settle(t, a, b)
	if b.readlink(".agents/skills/x") != "<no link>" || cur(b, ".agents/skills/x") != nil {
		t.Fatal("a link to an unsynced target was replicated")
	}
	if l, _ := linkStatus(a, ".agents/skills/x"); !strings.Contains(l.Reason, "not synced") {
		t.Fatalf("reason = %q", l.Reason)
	}
}

func TestD23_LinkTargetOutsideHomeSkipped(t *testing.T) {
	a, b := skillsFleet(t)
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	a.symlink(".agents/skills/out", outside)
	a.symlink(".agents/skills/up", "../../../../escape")
	for _, rel := range []string{".agents/skills/out", ".agents/skills/up"} {
		eventually(t, rel+" skipped", linkState(a, rel, LinkSkipped))
		if l, _ := linkStatus(a, rel); !strings.Contains(l.Reason, "outside") {
			t.Fatalf("%s reason = %q", rel, l.Reason)
		}
	}
	settle(t, a, b)
	if b.readlink(".agents/skills/out") != "<no link>" || b.readlink(".agents/skills/up") != "<no link>" {
		t.Fatal("a link outside ~ was replicated")
	}
}

func TestD23_LinkIntoOtherGroupSkippedAndPathNotLeaked(t *testing.T) {
	a, b, c, pair := pairFleet(t)
	a.write("repo/skills/foo/SKILL.md", "foo")
	if err := a.e.Add(a.path("repo/skills"), nil, pair); err != nil {
		t.Fatal(err)
	}
	a.write(".agents/skills/plain.md", "p")
	a.symlink(".agents/skills/foo", a.path("repo/skills/foo"))
	a.addLinks(".agents/skills") // main: a, b and c
	eventually(t, "c has the main entry", hasFile(c, ".agents/skills/plain.md", "p"))
	eventually(t, "a reports it", linkState(a, ".agents/skills/foo", LinkSkipped))
	if l, _ := linkStatus(a, ".agents/skills/foo"); !strings.Contains(l.Reason, "another group") {
		t.Fatalf("reason = %q", l.Reason)
	}
	settle(t, a, b, c)
	for _, m := range []*machine{b, c} {
		if cur(m, ".agents/skills/foo") != nil || m.readlink(".agents/skills/foo") != "<no link>" {
			t.Fatalf("%s got a link into another group", m.name)
		}
	}
	if hasEntry(c, "repo/skills") {
		t.Fatal("c learned the other group's path")
	}
}

// A link can reach a machine before its target (catch-up order, or here:
// b refuses the target's only file as oversized). It waits, and is
// created once the target arrives.
func TestD23_LinkPendingUntilTargetArrives(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b", func(o *Options) { o.MaxFileSize = 16 })
	a.write("repo/skills/foo/SKILL.md", "foo")
	a.write("repo/skills/later/BIG.md", strings.Repeat("x", 64)) // over b's limit
	a.write(".agents/skills/x.md", "x")
	a.symlink(".agents/skills/later", a.path("repo/skills/later"))
	a.add("repo/skills")
	a.addLinks(".agents/skills")
	link(t, a, b)
	eventually(t, "pending on b", linkState(b, ".agents/skills/later", LinkPending))
	if b.readlink(".agents/skills/later") != "<no link>" {
		t.Fatal("a dangling link was created")
	}
	a.write("repo/skills/later/SKILL.md", "later")
	eventually(t, "link created once its target arrived", hasLink(b, ".agents/skills/later", b.path("repo/skills/later")))
	eventually(t, "synced", linkState(b, ".agents/skills/later", LinkSynced))
}

// A link dangling on the machine that has it is not replicated (no peer
// waits for a target that may never come); it is once its target exists.
func TestD23_DanglingHereIsNotReplicated(t *testing.T) {
	a, b := skillsFleet(t)
	eventually(t, "first link on b", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	a.symlink(".agents/skills/later", a.path("repo/skills/later"))
	a.e.Rescan()
	eventually(t, "a reports it", linkState(a, ".agents/skills/later", LinkSkipped))
	if l, _ := linkStatus(a, ".agents/skills/later"); !strings.Contains(l.Reason, "not here") {
		t.Fatalf("reason = %q", l.Reason)
	}
	settle(t, a, b)
	if _, ok := linkStatus(b, ".agents/skills/later"); ok || cur(b, ".agents/skills/later") != nil {
		t.Fatal("a dangling link reached b")
	}
	a.write("repo/skills/later/SKILL.md", "later")
	a.e.Rescan()
	eventually(t, "link on b once its target exists", hasLink(b, ".agents/skills/later", b.path("repo/skills/later")))
}

func TestD23_RetargetPropagates(t *testing.T) {
	a, b := skillsFleet(t)
	eventually(t, "link on b", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	a.retarget(".agents/skills/foo", a.path("repo/skills/bar"))
	eventually(t, "retarget on b", hasLink(b, ".agents/skills/foo", b.path("repo/skills/bar")))
	if b.read(".agents/skills/foo/SKILL.md") != "bar" || b.read("repo/skills/foo/SKILL.md") != "foo" {
		t.Fatal("retarget touched what the links point at")
	}
}

func TestD23_ConcurrentRetargetLoserInTrash(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	for _, n := range []string{"foo", "bar", "baz"} {
		a.write("repo/skills/"+n+"/SKILL.md", n)
	}
	a.symlink(".agents/skills/s", "../../repo/skills/foo")
	a.add("repo/skills")
	a.addLinks(".agents/skills")
	cut := link(t, a, b)
	eventually(t, "link on b", hasLink(b, ".agents/skills/s", "../../repo/skills/foo"))
	eventually(t, "b has every target", hasFile(b, "repo/skills/baz/SKILL.md", "baz"))
	cut()
	eventually(t, "down", func() bool { return len(a.e.Status().Peers) == 0 && len(b.e.Status().Peers) == 0 })
	a.retarget(".agents/skills/s", "../../repo/skills/bar")
	a.e.Rescan()
	time.Sleep(1100 * time.Millisecond) // b's retarget is newer
	b.retarget(".agents/skills/s", "../../repo/skills/baz")
	b.e.Rescan()
	link(t, a, b)
	eventually(t, "b's newer retarget wins on a", hasLink(a, ".agents/skills/s", "../../repo/skills/baz"))
	settle(t, a, b)
	if b.readlink(".agents/skills/s") != "../../repo/skills/baz" {
		t.Fatal("b lost its winning retarget")
	}
	if !linkTrashed(a, "../../repo/skills/bar") {
		t.Fatal("the losing link is not in a's trash")
	}
	for _, m := range []*machine{a, b} {
		ents, _ := os.ReadDir(m.path(".agents/skills"))
		for _, e := range ents {
			if strings.Contains(e.Name(), ".conflict-") {
				t.Fatalf("%s got a conflict-copy link %s", m.name, e.Name())
			}
		}
	}
}

func TestD23_DeleteTrashesLinkObjectNotTarget(t *testing.T) {
	a, b := skillsFleet(t)
	eventually(t, "link on b", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	os.Remove(a.path(".agents/skills/foo"))
	eventually(t, "link gone on b", hasLink(b, ".agents/skills/foo", "<no link>"))
	if !linkTrashed(b, b.path("repo/skills/foo")) {
		t.Fatal("b's link is not in its trash")
	}
	settle(t, a, b)
	if b.read("repo/skills/foo/SKILL.md") != "foo" || a.read("repo/skills/foo/SKILL.md") != "foo" {
		t.Fatal("deleting the link touched its target")
	}
}

func TestD8_RetargetBeatsDelete(t *testing.T) {
	a, b := skillsFleet(t)
	eventually(t, "link on b", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	eventually(t, "bar on b", hasFile(b, "repo/skills/bar/SKILL.md", "bar"))
	cutAll(t, a, b)
	b.retarget(".agents/skills/foo", b.path("repo/skills/bar"))
	b.e.Rescan()
	time.Sleep(1100 * time.Millisecond) // the delete is newer: only D8 keeps the retarget
	os.Remove(a.path(".agents/skills/foo"))
	a.e.Rescan()
	link(t, a, b)
	eventually(t, "the retarget is back on a", hasLink(a, ".agents/skills/foo", a.path("repo/skills/bar")))
	settle(t, a, b)
	if b.readlink(".agents/skills/foo") != b.path("repo/skills/bar") {
		t.Fatal("b lost its retarget to the delete")
	}
}

func TestD23_LinkVsFolderReportedNeverClobbered(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("repo/skills/foo/SKILL.md", "foo")
	a.write(".agents/skills/x.md", "x")
	a.symlink(".agents/skills/foo", a.path("repo/skills/foo"))
	b.write(".agents/skills/foo/SKILL.md", "b's own")
	a.add("repo/skills")
	a.addLinks(".agents/skills")
	link(t, a, b)
	eventually(t, "b reports the link blocked", linkState(b, ".agents/skills/foo", LinkBlocked))
	settle(t, a, b)
	if b.read(".agents/skills/foo/SKILL.md") != "b's own" {
		t.Fatal("b's folder was clobbered")
	}
	if a.read("repo/skills/foo/SKILL.md") != "foo" || a.readlink(".agents/skills/foo") != a.path("repo/skills/foo") {
		t.Fatal("a's link or what it points at changed")
	}
}

func TestD22_KeepBothWithLinkVsFile(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("repo/skills/foo.md", "foo")
	a.symlink(".agents/skills/foo.md", a.path("repo/skills/foo.md"))
	b.write(".agents/skills/foo.md", "b's file")
	link(t, a, b)
	eventually(t, "a and b know each other", func() bool { return len(a.e.Machines()) == 2 && len(b.e.Machines()) == 2 })
	a.add("repo/skills")
	a.addLinks(".agents/skills")
	eventually(t, "link on b", hasLink(b, ".agents/skills/foo.md", b.path("repo/skills/foo.md")))
	eventually(t, "b's file is kept as a copy on a", func() bool {
		return len(conflictCopies(a, ".agents/skills", "foo")) == 1
	})
	for _, content := range conflictCopies(a, ".agents/skills", "foo") {
		if content != "b's file" {
			t.Fatalf("copy = %q", content)
		}
	}
}

func TestD13_ReplaceBacksUpFileBeforeLink(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("repo/skills/foo.md", "foo")
	a.symlink(".agents/skills/foo.md", a.path("repo/skills/foo.md"))
	b.write(".agents/skills/foo.md", "b's file")
	link(t, a, b)
	eventually(t, "a and b know each other", func() bool { return len(a.e.Machines()) == 2 && len(b.e.Machines()) == 2 })
	a.add("repo/skills")
	if err := a.e.AddEntry(a.path(".agents/skills"), AddOptions{Links: true, Policy: proto.FirstAddReplace}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "link on b", hasLink(b, ".agents/skills/foo.md", b.path("repo/skills/foo.md")))
	if !backedUp(b, "b's file") {
		t.Fatal("b's file was not backed up")
	}
	settle(t, a, b)
	if n := len(conflictCopies(a, ".agents/skills", "foo")) + len(conflictCopies(b, ".agents/skills", "foo")); n != 0 {
		t.Fatalf("%d conflict copies with this machine wins", n)
	}
}

func TestD23_LinkVsFileAfterAddNewerWinsFileKept(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("repo/skills/foo.md", "foo")
	a.write(".agents/skills/s.md", "v1")
	a.add("repo/skills")
	a.addLinks(".agents/skills")
	link(t, a, b)
	eventually(t, "b has the file", hasFile(b, ".agents/skills/s.md", "v1"))
	eventually(t, "b has the target", hasFile(b, "repo/skills/foo.md", "foo"))
	cutAll(t, a, b)
	b.write(".agents/skills/s.md", "b's edit")
	b.e.Rescan()
	time.Sleep(1100 * time.Millisecond) // a's link is newer
	os.Remove(a.path(".agents/skills/s.md"))
	a.symlink(".agents/skills/s.md", a.path("repo/skills/foo.md"))
	a.e.Rescan()
	link(t, a, b)
	eventually(t, "the newer link wins on b", hasLink(b, ".agents/skills/s.md", b.path("repo/skills/foo.md")))
	eventually(t, "b's edit kept as a copy", func() bool {
		for _, c := range conflictCopies(b, ".agents/skills", "s") {
			if c == "b's edit" {
				return true
			}
		}
		return false
	})
}

func TestD23_DanglingLinkReported(t *testing.T) {
	a, b := skillsFleet(t)
	eventually(t, "link on b", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	os.RemoveAll(a.path("repo/skills/foo"))
	eventually(t, "target gone on b", hasFile(b, "repo/skills/foo/SKILL.md", "<missing>"))
	for _, m := range []*machine{a, b} {
		eventually(t, m.name+" reports it dangling", linkState(m, ".agents/skills/foo", LinkDangling))
	}
	if b.readlink(".agents/skills/foo") != b.path("repo/skills/foo") || a.readlink(".agents/skills/foo") != a.path("repo/skills/foo") {
		t.Fatal("a dangling link was removed; Enigma never deletes what the user did not")
	}
}

func TestD23_LinksOffByDefaultAndToggled(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("repo/skills/foo/SKILL.md", "foo")
	a.write(".agents/skills/x.md", "x")
	a.symlink(".agents/skills/foo", a.path("repo/skills/foo"))
	a.add("repo/skills")
	a.add(".agents/skills")
	link(t, a, b)
	eventually(t, "x on b", hasFile(b, ".agents/skills/x.md", "x"))
	settle(t, a, b)
	if b.readlink(".agents/skills/foo") != "<no link>" {
		t.Fatal("a link synced with links off")
	}
	if err := a.e.SetLinks(a.path(".agents/skills"), true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "link on b once on", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	if err := a.e.SetLinks(a.path("repo/skills/foo/SKILL.md"), true); err == nil {
		t.Fatal("links on a path that is not a folder entry accepted")
	}
}

// A peer is trusted but may be buggy: the receiver checks a link's target
// again and never materializes one it would not have sent.
func TestD23_ReceiverRechecksEligibility(t *testing.T) {
	_, b := skillsFleet(t)
	eventually(t, "link on b", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	b.write("elsewhere/x/f", "exists, so only the eligibility check stops it")
	for _, target := range []string{"~/elsewhere/x", "../../../../outside", "/etc", "~/../x"} {
		v := version.Version{Hash: fsx.LinkHash(target), Vector: version.Vector{"id-a": 1000}, Mtime: time.Now().UnixNano(), By: "id-a", ByName: "a", Link: true}
		b.e.do(func() {
			p := b.e.peers["id-a"]
			b.e.onFile(p, ".agents/skills/bad", v, nil, []byte(target)) // synchronous: on b's loop
		})
		if b.readlink(".agents/skills/bad") != "<no link>" || cur(b, ".agents/skills/bad") != nil {
			t.Fatalf("b materialized an ineligible link to %s", target)
		}
	}
}

func cutAll(t *testing.T, ms ...*machine) {
	t.Helper()
	for _, m := range ms {
		m.e.do(func() {
			for _, p := range m.e.peers {
				p.close()
			}
		})
	}
	for _, m := range ms {
		eventually(t, m.name+" down", func() bool { return len(m.e.Status().Peers) == 0 })
	}
}
