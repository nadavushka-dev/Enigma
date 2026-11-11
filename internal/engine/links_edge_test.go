package engine

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/version"
)

// A link whose target is ignored by the target's entry is not what that
// entry syncs: skipped.
func TestD23_LinkToAnIgnoredTargetSkipped(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("repo/skills/foo/SKILL.md", "foo")
	a.write("repo/skills/scratch/x.md", "x")
	a.write(".agents/skills/plain.md", "p")
	a.symlink(".agents/skills/scratch", a.path("repo/skills/scratch"))
	a.add("repo/skills", "scratch")
	a.addLinks(".agents/skills")
	link(t, a, b)
	eventually(t, "skipped", linkState(a, ".agents/skills/scratch", LinkSkipped))
	if l, _ := linkStatus(a, ".agents/skills/scratch"); !strings.Contains(l.Reason, "ignored") {
		t.Fatalf("reason = %q", l.Reason)
	}
	eventually(t, "plain synced", hasFile(b, ".agents/skills/plain.md", "p"))
	settle(t, a, b)
	if b.readlink(".agents/skills/scratch") != "<no link>" {
		t.Fatal("a link to an ignored target was replicated")
	}
}

// A link counts toward the folder's file limit (§8): past it, a new link
// waits as over the limit.
func TestD23_LinkCountsTowardTheFileLimit(t *testing.T) {
	a := newMachine(t, "a", func(o *Options) { o.MaxFiles = 2; o.NoWatch = true })
	a.write("repo/skills/foo/SKILL.md", "foo")
	a.write(".agents/skills/one.md", "1")
	a.symlink(".agents/skills/l1", a.path("repo/skills/foo"))
	a.add("repo/skills")
	a.addLinks(".agents/skills") // one file and one link: at the limit
	a.symlink(".agents/skills/l2", a.path("repo/skills/foo"))
	a.e.Rescan()
	st := a.e.Status()
	var over []string
	for _, es := range st.Entries {
		if es.Path == ".agents/skills" {
			over = es.OverLimit
		}
	}
	if len(over) != 1 || over[0] != ".agents/skills/l2" || cur(a, ".agents/skills/l2") != nil || cur(a, ".agents/skills/l1") == nil {
		t.Fatalf("over limit %v", over)
	}
}

// A user retarget between the decision and the write is never overwritten:
// the write is abandoned, and the user's retarget wins as a new edit.
func TestD23_UserRetargetBetweenDecideAndApplyIsKept(t *testing.T) {
	a, b := skillsFleet(t)
	eventually(t, "link on b", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	eventually(t, "bar on b", hasFile(b, "repo/skills/bar/SKILL.md", "bar"))
	fired := make(chan struct{})
	var once sync.Once
	hook := func(machine, rel string, act Action) {
		if machine == "b" && rel == ".agents/skills/foo" && act == Apply {
			once.Do(func() { b.retarget(".agents/skills/foo", "../../repo/skills/bar"); close(fired) })
		}
	}
	afterDecide.Store(&hook)
	defer afterDecide.Store(nil)
	time.Sleep(10 * time.Millisecond) // a newer retarget on a
	a.retarget(".agents/skills/foo", a.path("repo/skills/plain-missing"))
	a.write("repo/skills/plain-missing/SKILL.md", "pm")
	a.e.Rescan()
	<-fired
	settle(t, a, b)
	if got := b.readlink(".agents/skills/foo"); got != "../../repo/skills/bar" {
		t.Fatalf("b's retarget made during the write was overwritten: %q", got)
	}
	eventually(t, "b's retarget reaches a", hasLink(a, ".agents/skills/foo", "../../repo/skills/bar"))
}

// A link replaced by a file between the decision and the write: the file
// is never overwritten or trashed.
func TestD23_LinkReplacedByAFileBetweenDecideAndApplyIsKept(t *testing.T) {
	a, b := skillsFleet(t)
	eventually(t, "link on b", hasLink(b, ".agents/skills/foo", b.path("repo/skills/foo")))
	fired := make(chan struct{})
	var once sync.Once
	hook := func(machine, rel string, act Action) {
		if machine == "b" && rel == ".agents/skills/foo" && act == Apply {
			once.Do(func() {
				os.Remove(b.path(".agents/skills/foo"))
				b.write(".agents/skills/foo", "user file on b")
				close(fired)
			})
		}
	}
	afterDecide.Store(&hook)
	defer afterDecide.Store(nil)
	a.retarget(".agents/skills/foo", a.path("repo/skills/bar"))
	a.e.Rescan()
	<-fired
	settle(t, a, b)
	kept := b.read(".agents/skills/foo") == "user file on b"
	for _, c := range conflictCopies(b, ".agents/skills", "foo") {
		kept = kept || c == "user file on b"
	}
	if !kept {
		t.Fatalf("the user's file was lost: %q", b.read(".agents/skills/foo"))
	}
}

// The receiver's other checks: a link to an entry with links off, and a
// link whose content does not match its hash, are dropped.
func TestD23_ReceiverDropsLinksForOffEntriesAndBadHashes(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("repo/skills/foo/SKILL.md", "foo")
	a.write(".agents/skills/x.md", "x")
	a.add("repo/skills")
	a.add(".agents/skills") // links off
	link(t, a, b)
	eventually(t, "x on b", hasFile(b, ".agents/skills/x.md", "x"))
	good := "~/repo/skills/foo"
	v := version.Version{Hash: fsx.LinkHash(good), Vector: version.Vector{"id-a": 1000}, Mtime: time.Now().UnixNano(), By: "id-a", ByName: "a", Link: true}
	b.e.do(func() {
		p := b.e.peers["id-a"]
		b.e.onAnnounce(p, ".agents/skills/off", v)
		b.e.onFile(p, ".agents/skills/off", v, nil, []byte(good))
	})
	if b.readlink(".agents/skills/off") != "<no link>" || cur(b, ".agents/skills/off") != nil {
		t.Fatal("a link was applied in an entry with links off")
	}
	if err := b.e.SetLinks(b.path(".agents/skills"), true); err != nil {
		t.Fatal(err)
	}
	b.e.do(func() {
		p := b.e.peers["id-a"]
		b.e.onFile(p, ".agents/skills/bad", v, nil, []byte("~/repo/skills/other"))
	})
	if b.readlink(".agents/skills/bad") != "<no link>" || cur(b, ".agents/skills/bad") != nil {
		t.Fatal("a link whose target does not match its hash was applied")
	}
}

func TestD23_SetLinksRefusals(t *testing.T) {
	a, _, c, pair := pairFleet(t)
	a.write("f.md", "x")
	a.add("f.md")
	if err := a.e.SetLinks(a.path("f.md"), true); err == nil || !strings.Contains(err.Error(), "is a file") {
		t.Fatalf("file entry: %v", err)
	}
	a.write("p/x.md", "x")
	if err := a.e.Add(a.path("p"), nil, pair); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(c.path("p"), 0o755)
	if err := c.e.SetLinks(c.path("p"), true); err == nil { // c is not in pair
		t.Fatal("c set links on an entry it does not sync")
	}
	a.write("d/x.md", "x")
	a.addLinks("d")
	if err := a.e.SetLinks(a.path("d"), true); err != nil {
		t.Fatalf("setting what is already set: %v", err)
	}
}

// The restore of a preexisting copy when it is in neither the disk nor
// history: nothing to keep, but the peer's version is still written.
func TestD13_RestoreWithoutHistoryStillRestores(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("d/x.md", "x")
	b.write("d/sub/extra.md", "b's extra")
	a.add("d")
	link(t, a, b)
	eventually(t, "a has b's extra", hasFile(a, "d/sub/extra.md", "b's extra"))
	moveBehindLink(t, b, "d/sub", "repo/sub")
	a.write("d/sub/extra.md", "a's edit")
	settle(t, a, b)
	b.e.do(func() { os.RemoveAll(filepath.Join(b.data, "history")) })
	os.Remove(b.path("d/sub"))
	b.e.Rescan()
	eventually(t, "b restores a's edit", hasFile(b, "d/sub/extra.md", "a's edit"))
	if b.read("repo/sub/extra.md") != "b's extra" {
		t.Fatal("the repo copy changed")
	}
}

// Removing an entry drops the links waiting on it.
func TestD23_RemovingTheEntryDropsPendingLinks(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b", func(o *Options) { o.MaxFileSize = 16 })
	a.write("repo/skills/later/BIG.md", strings.Repeat("x", 64))
	a.write(".agents/skills/x.md", "x")
	a.symlink(".agents/skills/later", a.path("repo/skills/later"))
	a.add("repo/skills")
	a.addLinks(".agents/skills")
	link(t, a, b)
	eventually(t, "pending on b", linkState(b, ".agents/skills/later", LinkPending))
	if err := a.e.Remove(a.path(".agents/skills")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b drops the entry", func() bool { return !hasEntry(b, ".agents/skills") })
	var n int
	b.e.do(func() { n = len(b.e.pending) })
	if n != 0 {
		t.Fatalf("%d pending links left after the entry was removed", n)
	}
	b.write("repo/skills/later/SKILL.md", "now here")
	b.e.Rescan()
	settle(t, a, b)
	if b.readlink(".agents/skills/later") != "<no link>" {
		t.Fatal("a link was created for a removed entry")
	}
}
