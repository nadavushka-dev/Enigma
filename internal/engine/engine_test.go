package engine

import (
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- harness ---------------------------------------------------------------

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

type machine struct {
	t       *testing.T
	name    string
	home    string
	data    string
	e       *Engine
	noWatch bool
	opts    func(*Options)
}

func newMachine(t *testing.T, name string, mods ...func(*Options)) *machine {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	m := &machine{t: t, name: name, home: filepath.Join(base, "home"), data: filepath.Join(base, "data")}
	os.MkdirAll(m.home, 0o755)
	m.opts = func(o *Options) {
		for _, f := range mods {
			f(o)
		}
	}
	m.start()
	return m
}

func (m *machine) start() {
	m.t.Helper()
	o := Options{Home: m.home, DataDir: m.data, MachineID: "id-" + m.name, Name: m.name,
		FleetID: "fleet-1", Debounce: 30 * time.Millisecond, Log: quietLog}
	m.opts(&o)
	m.noWatch = o.NoWatch
	e, err := New(o)
	if err != nil {
		m.t.Fatal(err)
	}
	m.e = e
	m.t.Cleanup(func() { m.e.Close() })
}

func (m *machine) restart() {
	m.e.Close()
	m.start()
}

func (m *machine) path(rel string) string { return filepath.Join(m.home, filepath.FromSlash(rel)) }

func (m *machine) write(rel, content string) {
	m.t.Helper()
	p := m.path(rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) writeAged(rel, content string, age time.Duration) {
	m.write(rel, content)
	ts := time.Now().Add(-age)
	os.Chtimes(m.path(rel), ts, ts)
}

func (m *machine) read(rel string) string {
	b, err := os.ReadFile(m.path(rel))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func (m *machine) add(rel string, ignore ...string) {
	m.t.Helper()
	if err := m.e.Add(m.path(rel), ignore, ""); err != nil {
		m.t.Fatal(err)
	}
}

// link connects two machines over an in-process pipe and returns a func
// that cuts the link.
func link(t *testing.T, a, b *machine) (cut func()) {
	t.Helper()
	c1, c2 := net.Pipe()
	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() { defer wg.Done(); errA = a.e.Connect(c1, true) }()
	go func() { defer wg.Done(); errB = b.e.Connect(c2, false) }()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("connect: %v / %v", errA, errB)
	}
	return func() { c1.Close(); c2.Close() }
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func hasFile(m *machine, rel, content string) func() bool {
	return func() bool { return m.read(rel) == content }
}

func trashed(m *machine, content string) bool {
	found := false
	filepath.Walk(filepath.Join(m.data, "trash"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); string(b) == content {
				found = true
			}
		}
		return nil
	})
	return found
}

func backedUp(m *machine, content string) bool {
	found := false
	filepath.Walk(filepath.Join(m.data, "backups"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); string(b) == content {
				found = true
			}
		}
		return nil
	})
	return found
}

func conflictCopies(m *machine, dirRel, stem string) map[string]string {
	out := map[string]string{}
	entries, _ := os.ReadDir(m.path(dirRel))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stem+".conflict-") {
			out[e.Name()] = m.read(filepath.ToSlash(filepath.Join(dirRel, e.Name())))
		}
	}
	return out
}

// ---- D6: entries appear on every machine -----------------------------------

func TestD6_AddedFileIsCreatedOnPeerWithParents(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write(".claude/CLAUDE.md", "# rules")
	a.add(".claude/CLAUDE.md")
	link(t, a, b)
	eventually(t, "file on b", hasFile(b, ".claude/CLAUDE.md", "# rules"))
}

func TestD6_EntryAddedWhileLinkedReachesPeer(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	link(t, a, b)
	a.write("notes/todo.md", "1")
	a.add("notes")
	eventually(t, "folder on b", hasFile(b, "notes/todo.md", "1"))
}

func TestD6_NewFilesInFolderSyncBothWays(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("skills/one.md", "1")
	a.add("skills")
	link(t, a, b)
	eventually(t, "initial", hasFile(b, "skills/one.md", "1"))
	b.write("skills/deep/two.md", "2")
	eventually(t, "b->a new nested file", hasFile(a, "skills/deep/two.md", "2"))
}

// ---- D2: real-time edits ---------------------------------------------------

func TestD2_EditsFlowBothWays(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("f.md", "v1")
	a.add("f.md")
	link(t, a, b)
	eventually(t, "v1 on b", hasFile(b, "f.md", "v1"))
	a.write("f.md", "v2 from a")
	eventually(t, "v2 on b", hasFile(b, "f.md", "v2 from a"))
	b.write("f.md", "v3 from b")
	eventually(t, "v3 on a", hasFile(a, "f.md", "v3 from b"))
}

func TestD2_ExecBitSyncs(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("bin/run.sh", "#!/bin/sh")
	os.Chmod(a.path("bin/run.sh"), 0o755)
	a.add("bin")
	link(t, a, b)
	eventually(t, "exec on b", func() bool {
		fi, err := os.Stat(b.path("bin/run.sh"))
		return err == nil && fi.Mode().Perm()&0o100 != 0
	})
}

func TestNoEchoAfterSync(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("f.md", "v1")
	a.add("f.md")
	link(t, a, b)
	eventually(t, "synced", hasFile(b, "f.md", "v1"))
	a.write("f.md", "v2")
	eventually(t, "synced v2", hasFile(b, "f.md", "v2"))
	time.Sleep(300 * time.Millisecond) // let watcher echoes arrive
	before := a.e.Status().Sent + b.e.Status().Sent
	time.Sleep(400 * time.Millisecond)
	after := a.e.Status().Sent + b.e.Status().Sent
	if after != before {
		t.Fatalf("machines keep talking after sync settled: %d -> %d messages", before, after)
	}
}

// ---- D8: deletes ------------------------------------------------------------

func TestD8_DeleteSpreadsAndPeerTrashesItsCopy(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("dir/sub/x.md", "bye")
	a.write("dir/keep.md", "stay")
	a.add("dir")
	link(t, a, b)
	eventually(t, "synced", hasFile(b, "dir/sub/x.md", "bye"))
	os.Remove(a.path("dir/sub/x.md"))
	eventually(t, "deleted on b", hasFile(b, "dir/sub/x.md", "<missing>"))
	if !trashed(b, "bye") {
		t.Fatal("b must keep its copy in the trash")
	}
	if _, err := os.Stat(b.path("dir/sub")); !os.IsNotExist(err) {
		t.Fatal("empty folder left behind on b")
	}
	if b.read("dir/keep.md") != "stay" {
		t.Fatal("unrelated file touched")
	}
}

func TestD8_EditBeatsDeleteAcrossOffline(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("n/f.md", "v1")
	a.add("n")
	cut := link(t, a, b)
	eventually(t, "synced", hasFile(b, "n/f.md", "v1"))
	cut()
	os.Remove(a.path("n/f.md"))
	b.write("n/f.md", "edited on b")
	time.Sleep(200 * time.Millisecond) // both record their change offline
	link(t, a, b)
	eventually(t, "edit restored on a", hasFile(a, "n/f.md", "edited on b"))
	eventually(t, "edit kept on b", hasFile(b, "n/f.md", "edited on b"))
}

// ---- D4: conflicts (no merge yet: newer wins, loser kept as a copy) ---------

func TestD4_ConcurrentEditsNewerWinsAndCopyIsEverywhere(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("c/CLAUDE.md", "base")
	a.add("c")
	cut := link(t, a, b)
	eventually(t, "synced", hasFile(b, "c/CLAUDE.md", "base"))
	cut()
	a.writeAged("c/CLAUDE.md", "older edit on a", time.Hour)
	b.write("c/CLAUDE.md", "newer edit on b")
	time.Sleep(200 * time.Millisecond)
	link(t, a, b)
	eventually(t, "winner on a", hasFile(a, "c/CLAUDE.md", "newer edit on b"))
	eventually(t, "copies converge", func() bool {
		ca, cb := conflictCopies(a, "c", "CLAUDE"), conflictCopies(b, "c", "CLAUDE")
		if len(ca) != 1 || len(cb) != 1 {
			return false
		}
		for name, content := range ca {
			return cb[name] == content && content == "older edit on a"
		}
		return false
	})
	if len(a.e.Status().Conflicts)+len(b.e.Status().Conflicts) == 0 {
		t.Fatal("conflict not reported in status")
	}
}

// ---- D7 / D13 / D14: first add ------------------------------------------------

func TestD7_D13_FirstAddWinsAndPeerBacksUpItsCopy(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.writeAged(".claude/CLAUDE.md", "adder version", time.Hour) // older, still wins
	b.write(".claude/CLAUDE.md", "b's own version")
	link(t, a, b)
	a.add(".claude/CLAUDE.md")
	eventually(t, "adder wins on b", hasFile(b, ".claude/CLAUDE.md", "adder version"))
	if !backedUp(b, "b's own version") {
		t.Fatal("b must back up its differing copy locally")
	}
	if backedUp(a, "adder version") || backedUp(a, "b's own version") {
		t.Fatal("backups are local to the machine whose copy lost")
	}
	if c := conflictCopies(b, ".claude", "CLAUDE"); len(c) != 0 {
		t.Fatalf("first add must not create conflict copies: %v", c)
	}
}

func TestD14_FolderFirstAddKeepsAndSpreadsExtras(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("skills/a.md", "from a")
	a.write("skills/both.md", "a wins")
	b.write("skills/b-only.md", "from b")
	b.write("skills/both.md", "b loses")
	link(t, a, b)
	a.add("skills")
	eventually(t, "a's file on b", hasFile(b, "skills/a.md", "from a"))
	eventually(t, "b's extra on a", hasFile(a, "skills/b-only.md", "from b"))
	eventually(t, "shared file: adder wins", hasFile(b, "skills/both.md", "a wins"))
	if a.read("skills/b-only.md") != "from b" || b.read("skills/b-only.md") != "from b" {
		t.Fatal("extra must survive on both")
	}
	if !backedUp(b, "b loses") {
		t.Fatal("b's differing copy must be backed up")
	}
}

func TestD7_SameContentNeedsNoBackup(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("f.md", "same")
	b.write("f.md", "same")
	link(t, a, b)
	a.add("f.md")
	time.Sleep(300 * time.Millisecond)
	if backedUp(b, "same") {
		t.Fatal("identical copy must not be backed up")
	}
}

// ---- D15 / D16 / D18 and ignores ---------------------------------------------

func TestD15_SymlinksAreSkippedAndReported(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("d/real.md", "x")
	os.Symlink(a.path("d/real.md"), a.path("d/link.md"))
	a.add("d")
	link(t, a, b)
	eventually(t, "real synced", hasFile(b, "d/real.md", "x"))
	if _, err := os.Lstat(b.path("d/link.md")); !os.IsNotExist(err) {
		t.Fatal("symlink must not sync")
	}
	if !contains(a.e.Status().Entries[0].Skipped, "d/link.md") {
		t.Fatalf("symlink not reported: %+v", a.e.Status().Entries)
	}
}

func TestD16_PathsOutsideHomeAreRefused(t *testing.T) {
	a := newMachine(t, "a")
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	os.WriteFile(filepath.Join(outside, "f"), []byte("x"), 0o644)
	if err := a.e.Add(filepath.Join(outside, "f"), nil, ""); err == nil {
		t.Fatal("path outside ~ accepted")
	}
}

func TestD18_OversizedFileIsSkippedAndReported(t *testing.T) {
	small := func(o *Options) { o.MaxFileSize = 8 }
	a, b := newMachine(t, "a", small), newMachine(t, "b", small)
	a.write("d/ok.md", "fine")
	a.write("d/big.bin", "way too large")
	a.add("d")
	link(t, a, b)
	eventually(t, "ok synced", hasFile(b, "d/ok.md", "fine"))
	time.Sleep(100 * time.Millisecond)
	if b.read("d/big.bin") != "<missing>" {
		t.Fatal("oversized file synced")
	}
	if !contains(a.e.Status().Entries[0].Skipped, "d/big.bin") {
		t.Fatal("oversized file not reported")
	}
}

func TestD18_FolderWithTooManyFilesIsRefused(t *testing.T) {
	a := newMachine(t, "a", func(o *Options) { o.MaxFiles = 2 })
	for _, n := range []string{"1", "2", "3"} {
		a.write("d/"+n, n)
	}
	if err := a.e.Add(a.path("d"), nil, ""); err == nil {
		t.Fatal("folder over the file limit accepted")
	}
}

// Regression: Add hashed every file of a folder before counting them, on
// the engine loop, so a huge folder froze the daemon (links dropped as
// silent). Unreadable files show the count never opens them.
func TestFolderOverTheLimitIsRefusedWithoutReadingIt(t *testing.T) {
	a := newMachine(t, "a", func(o *Options) { o.MaxFiles = 2 })
	for _, n := range []string{"1", "2", "3"} {
		a.write("d/"+n, n)
		if err := os.Chmod(a.path("d/"+n), 0); err != nil {
			t.Fatal(err)
		}
	}
	err := a.e.Add(a.path("d"), nil, "")
	if err == nil || !strings.Contains(err.Error(), "more than 2 files") {
		t.Fatalf("Add = %v, want the file-limit error", err)
	}
}

func TestIgnoredFilesDoNotSync(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("p/keep.md", "k")
	a.write("p/debug.log", "nope")
	a.write("p/.git/HEAD", "nope")
	a.write("p/.DS_Store", "nope")
	a.add("p", "*.log")
	link(t, a, b)
	eventually(t, "keep synced", hasFile(b, "p/keep.md", "k"))
	time.Sleep(100 * time.Millisecond)
	for _, rel := range []string{"p/debug.log", "p/.git/HEAD", "p/.DS_Store"} {
		if b.read(rel) != "<missing>" {
			t.Errorf("%s synced", rel)
		}
	}
}

func TestOverlappingEntriesAreRefused(t *testing.T) {
	a := newMachine(t, "a")
	a.write(".claude/skills/x.md", "x")
	a.write(".claude/CLAUDE.md", "y")
	a.add(".claude/skills")
	if err := a.e.Add(a.path(".claude/skills/x.md"), nil, ""); err == nil {
		t.Fatal("entry inside an entry accepted")
	}
	if err := a.e.Add(a.path(".claude"), nil, ""); err == nil {
		t.Fatal("entry containing an entry accepted")
	}
	if err := a.e.Add(a.path(".claude/CLAUDE.md"), nil, ""); err != nil {
		t.Fatalf("sibling entry refused: %v", err)
	}
}

// ---- §6.5: catch-up, relay, restart, vanished root ----------------------------

func TestCatchUpAfterOffline(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("d/1.md", "1")
	a.write("d/2.md", "2")
	a.add("d")
	cut := link(t, a, b)
	eventually(t, "synced", hasFile(b, "d/2.md", "2"))
	cut()
	a.write("d/1.md", "1 edited offline")
	os.Remove(a.path("d/2.md"))
	b.write("d/3.md", "new on b")
	time.Sleep(200 * time.Millisecond)
	link(t, a, b)
	eventually(t, "edit on b", hasFile(b, "d/1.md", "1 edited offline"))
	eventually(t, "delete on b", hasFile(b, "d/2.md", "<missing>"))
	eventually(t, "new on a", hasFile(a, "d/3.md", "new on b"))
}

func TestD3_ChangeIsRelayedAroundADeadLink(t *testing.T) {
	a, b, c := newMachine(t, "a"), newMachine(t, "b"), newMachine(t, "c")
	link(t, a, b)
	link(t, b, c) // no a–c link
	a.write("r.md", "via b")
	a.add("r.md")
	eventually(t, "c gets a's file through b", hasFile(c, "r.md", "via b"))
	c.write("r.md", "back from c")
	eventually(t, "a gets c's edit through b", hasFile(a, "r.md", "back from c"))
}

func TestRestartDetectsChangesMadeWhileStopped(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("d/edit.md", "v1")
	a.write("d/gone.md", "x")
	a.add("d")
	cut := link(t, a, b)
	eventually(t, "synced", hasFile(b, "d/gone.md", "x"))
	cut()
	a.e.Close()
	a.write("d/edit.md", "edited while daemon down")
	os.Remove(a.path("d/gone.md"))
	a.start()
	link(t, a, b)
	eventually(t, "offline edit reaches b", hasFile(b, "d/edit.md", "edited while daemon down"))
	eventually(t, "offline delete reaches b", hasFile(b, "d/gone.md", "<missing>"))
}

func TestStatePersistsAcrossRestart(t *testing.T) {
	a := newMachine(t, "a")
	a.write("f.md", "x")
	a.add("f.md")
	a.restart()
	st := a.e.Status()
	if len(st.Entries) != 1 || st.Entries[0].Path != "f.md" || st.Files != 1 {
		t.Fatalf("state lost on restart: %+v", st)
	}
}

func TestVanishedRootDoesNotDeleteEverywhere(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("vault/1.md", "1")
	a.write("vault/2.md", "2")
	a.add("vault")
	link(t, a, b)
	eventually(t, "synced", hasFile(b, "vault/2.md", "2"))
	// An unmounted disk vanishes at once. (RemoveAll would unlink files one
	// by one, and a stall between unlinks is a genuine delete of one file.)
	if err := os.Rename(b.path("vault"), filepath.Join(filepath.Dir(b.home), "unmounted")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	b.e.Rescan()
	time.Sleep(200 * time.Millisecond)
	if a.read("vault/1.md") != "1" || a.read("vault/2.md") != "2" {
		t.Fatal("vanished root propagated as deletes")
	}
	if !b.e.Status().Entries[0].Missing {
		t.Fatal("entry not flagged as missing locally")
	}
}

func TestRemoveEntryStopsSyncAndKeepsFiles(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("f.md", "v1")
	a.add("f.md")
	link(t, a, b)
	eventually(t, "synced", hasFile(b, "f.md", "v1"))
	if err := a.e.Remove(a.path("f.md")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b drops the entry", func() bool { return len(b.e.Status().Entries) == 0 })
	a.write("f.md", "v2 after removal")
	time.Sleep(300 * time.Millisecond)
	if b.read("f.md") != "v1" || a.read("f.md") != "v2 after removal" {
		t.Fatalf("files must stay and stop syncing: a=%q b=%q", a.read("f.md"), b.read("f.md"))
	}
}

func TestHandshakeRejectsOtherFleet(t *testing.T) {
	a := newMachine(t, "a")
	b := newMachine(t, "b", func(o *Options) { o.FleetID = "other" })
	c1, c2 := net.Pipe()
	errs := make(chan error, 2)
	go func() { errs <- a.e.Connect(c1, true) }()
	go func() { errs <- b.e.Connect(c2, false) }()
	if e1, e2 := <-errs, <-errs; e1 == nil || e2 == nil {
		t.Fatalf("cross-fleet link accepted: %v / %v", e1, e2)
	}
}

// Regression (simulation seed 1083): a user edit landing between the
// decision and its execution was overwritten, because the write's
// precondition was re-read from disk instead of taken from the decision.
func TestUserEditBetweenDecideAndApplyIsKept(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("f.md", "v1")
	a.add("f.md")
	link(t, a, b)
	eventually(t, "synced", hasFile(b, "f.md", "v1"))

	fired := make(chan struct{})
	var once sync.Once
	hook := func(machine, rel string, act Action) {
		if machine == "b" && rel == "f.md" && act == Apply {
			once.Do(func() { b.write("f.md", "user edit on b"); close(fired) })
		}
	}
	afterDecide.Store(&hook)
	defer afterDecide.Store(nil)

	a.write("f.md", "v2 from a")
	<-fired
	eventually(t, "machines settle", func() bool { return a.read("f.md") == b.read("f.md") })
	time.Sleep(300 * time.Millisecond)
	survived := a.read("f.md") == "user edit on b"
	for _, c := range conflictCopies(a, ".", "f") {
		survived = survived || c == "user edit on b"
	}
	if !survived {
		t.Fatalf("user edit lost: a=%q b=%q", a.read("f.md"), b.read("f.md"))
	}
}

// SPEC §5: both machines may dial each other; exactly one link survives on
// both sides, the one dialed by the lower machine ID ("id-a" < "id-b").
func TestOneLinkPerPairKeepsLowerIDsDial(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a1, b1 := net.Pipe() // dialed by a
	b2, a2 := net.Pipe() // dialed by b
	var wg sync.WaitGroup
	errs := make([]error, 4)
	wg.Add(4)
	go func() { defer wg.Done(); errs[0] = a.e.Connect(a1, true) }()
	go func() { defer wg.Done(); errs[1] = b.e.Connect(b1, false) }()
	go func() { defer wg.Done(); errs[2] = b.e.Connect(b2, true) }()
	go func() { defer wg.Done(); errs[3] = a.e.Connect(a2, false) }()
	wg.Wait()
	eventually(t, "one link each side", func() bool {
		return len(a.e.Status().Peers) == 1 && len(b.e.Status().Peers) == 1
	})
	// The link b dialed must be closed on both ends.
	eventually(t, "b's dial is closed", func() bool {
		_, err := b2.Write([]byte{0})
		return err != nil
	})
	if _, err := a1.Write(nil); err != nil {
		t.Fatalf("a's dial must stay open: %v", err)
	}
	a.write("f.md", "over the kept link")
	a.add("f.md")
	eventually(t, "sync works", hasFile(b, "f.md", "over the kept link"))
}

func TestReconnectFromSameSideReplacesOldLink(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	link(t, a, b)
	link(t, a, b) // e.g. a reconnects before noticing the old link died
	eventually(t, "still one link", func() bool {
		return len(a.e.Status().Peers) == 1 && len(b.e.Status().Peers) == 1
	})
	a.write("g.md", "x")
	a.add("g.md")
	eventually(t, "sync works", hasFile(b, "g.md", "x"))
}

// Regression: the root vanished while a peer's file was being applied, and
// the apply re-created the root folder (D6 creates missing parents). The
// root then looked present, so the files already there read as deletes.
func TestApplyDoesNotRecreateAVanishedRoot(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	a.write("vault/1.md", "1")
	a.write("vault/2.md", "2")
	a.add("vault")

	var applies atomic.Int32
	hook := func(machine, rel string, act Action) {
		if machine == "b" && act == Apply && applies.Add(1) == 2 {
			os.Rename(b.path("vault"), filepath.Join(filepath.Dir(b.home), "unmounted"))
		}
	}
	afterDecide.Store(&hook)
	defer afterDecide.Store(nil)

	link(t, a, b)
	eventually(t, "b noticed the vanished root", func() bool {
		st := b.e.Status()
		return len(st.Entries) == 1 && st.Entries[0].Missing
	})
	time.Sleep(300 * time.Millisecond)
	if a.read("vault/1.md") != "1" || a.read("vault/2.md") != "2" {
		t.Fatal("vanished root propagated as deletes")
	}
	if _, err := os.Stat(b.path("vault")); err == nil {
		t.Fatal("the vanished root was re-created")
	}
}

// Regression: a home folder reached through a symlink (macOS /tmp is
// /private/tmp; network homes) refused every path the CLI sent, because
// the engine resolved its own home but not the paths it was given.
func TestHomeBehindASymlinkAcceptsItsPaths(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	real := filepath.Join(base, "real-home")
	os.MkdirAll(filepath.Join(real, ".claude"), 0o755)
	os.WriteFile(filepath.Join(real, ".claude", "CLAUDE.md"), []byte("x"), 0o644)
	link := filepath.Join(base, "home")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	e, err := New(Options{Home: link, DataDir: filepath.Join(base, "data"), MachineID: "id-a", FleetID: "g", Log: quietLog, NoWatch: true})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	for _, p := range []string{filepath.Join(link, ".claude"), filepath.Join(real, ".claude", "CLAUDE.md")} {
		if err := e.Add(p, nil, ""); err != nil {
			t.Fatalf("add %s: %v", p, err)
		}
		if err := e.Remove(p); err != nil {
			t.Fatalf("remove %s: %v", p, err)
		}
	}
	if err := e.Add(filepath.Join(base, "elsewhere"), nil, ""); err == nil || !strings.Contains(err.Error(), "inside your home") {
		t.Fatalf("outside home: %v", err)
	}
}
