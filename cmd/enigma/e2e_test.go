package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// End-to-end through the real binary: several "machines" on this host,
// each with its own HOME, ENIGMA_HOME and daemon process. A fake ssh maps
// `ssh <alias> <cmd>` (and ssh://user@<alias>:port) onto the machine named
// <alias>, so machine add, bridge and the daemons' dialing all run for
// real, minus the network. ENIGMA_SERVICE=background everywhere: no fake
// machine ever touches this Mac's launchd.

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "enigma-e2e-bin")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "enigma")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		panic(fmt.Sprintf("build: %v\n%s", err, out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type world struct {
	t    *testing.T
	root string
	ssh  string
}

type host struct {
	w     *world
	name  string
	home  string
	eh    string   // ENIGMA_HOME
	bin   string   // enigma binary this machine's CLI runs (default: the test build)
	extra []string // more environment for its CLI and daemon
}

func newWorld(t *testing.T) *world {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	w := &world{t: t, root: root, ssh: filepath.Join(root, "ssh")}
	// Fake ssh: skip options, take the alias, run the command as that machine.
	script := `#!/bin/sh
if [ "$1" = "-G" ]; then printf 'user dev\nhostname %s\nport 22\n' "$2"; exit 0; fi
while [ $# -gt 0 ]; do case "$1" in -T) shift;; -o) shift 2;; *) break;; esac; done
alias="$1"; shift
case "$alias" in ssh://*) alias="${alias#ssh://}"; alias="${alias#*@}"; alias="${alias%:*}";; esac
dir="` + root + `/machines/$alias"
[ -d "$dir" ] || { echo "ssh: Could not resolve hostname $alias" >&2; exit 255; }
caller=$(basename "$(dirname "$HOME")")
[ -e "$dir/down" ] || [ -e "$dir/block-$caller" ] && { echo "ssh: connect to host $alias port 22: Connection refused" >&2; exit 255; }
export HOME="$dir/home" ENIGMA_HOME="$dir/enigma" ENIGMA_SERVICE=background ENIGMA_SSH="` + w.ssh + `"
exec sh -c "$*"
`
	os.WriteFile(w.ssh, []byte(script), 0o755)
	return w
}

func (w *world) host(name string) *host {
	h := &host{w: w, name: name, home: filepath.Join(w.root, "machines", name, "home"), eh: filepath.Join(w.root, "machines", name, "enigma"), bin: bin}
	os.MkdirAll(h.home, 0o755)
	quiet(w.t, h.eh)
	return h
}

// quiet turns desktop notifications off for an Enigma home: these tests run
// the real daemon, and its conflicts must not pop up on the developer's
// desktop (SPEC §12.3).
func quiet(t *testing.T, enigmaHome string) {
	t.Helper()
	os.MkdirAll(enigmaHome, 0o755)
	if err := os.WriteFile(filepath.Join(enigmaHome, "config.toml"), []byte("notifications = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildVersion builds enigma stamped with version, under dir.
func buildVersion(t *testing.T, dir, version string) string {
	t.Helper()
	out := filepath.Join(dir, version, "enigma")
	cmd := exec.Command("go", "build", "-ldflags", "-X github.com/nadavushka-dev/enigma/internal/buildinfo.Version="+version, "-o", out, ".")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", version, err, b)
	}
	return out
}

func (h *host) env() []string {
	return append(append(os.Environ(), "HOME="+h.home, "ENIGMA_HOME="+h.eh, "ENIGMA_SSH="+h.w.ssh, "ENIGMA_SERVICE=background"), h.extra...)
}

// run executes enigma as this machine and returns combined output and exit code.
func (h *host) run(args ...string) (string, int) {
	cmd := exec.Command(h.bin, args...)
	cmd.Env, cmd.Dir = h.env(), h.home
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		h.w.t.Fatalf("run %v: %v", args, err)
	}
	return out.String(), code
}

func (h *host) must(args ...string) string {
	h.w.t.Helper()
	out, code := h.run(args...)
	if code != 0 {
		h.w.t.Fatalf("%s: enigma %s: exit %d\n%s", h.name, strings.Join(args, " "), code, out)
	}
	return out
}

// startDaemon starts this machine's daemon; stop (also run at cleanup)
// stops it.
func (h *host) startDaemon() (stop func()) {
	h.w.t.Helper()
	cmd := exec.Command(h.bin, "daemon")
	cmd.Env, cmd.Dir = h.env(), h.home
	logf, _ := os.Create(filepath.Join(h.w.root, h.name+"-daemon.log"))
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		h.w.t.Fatal(err)
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cmd.Process.Signal(os.Interrupt)
			done := make(chan struct{})
			go func() { cmd.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
			}
			logf.Close()
		})
	}
	h.w.t.Cleanup(func() {
		stop()
		if h.w.t.Failed() {
			b, _ := os.ReadFile(logf.Name())
			h.w.t.Logf("--- %s daemon log ---\n%s", h.name, b)
		}
	})
	eventuallyT(h.w.t, h.name+" daemon up", func() bool { _, code := h.run("status"); return code == 0 })
	return stop
}

// add adds machine b to h's fleet with `enigma machine add`, the way a
// user would. b's daemon then runs in the background (from the binary
// installed in b's ~/.local/bin) until the test ends.
func (h *host) add(b *host, extra ...string) string {
	h.w.t.Helper()
	h.w.t.Cleanup(func() {
		b.run("service", "uninstall")
		if h.w.t.Failed() {
			log, _ := os.ReadFile(filepath.Join(b.eh, "enigma.log"))
			h.w.t.Logf("--- %s daemon log (background) ---\n%s", b.name, log)
		}
	})
	return h.must(append([]string{"machine", "add", b.name}, extra...)...)
}

// groupMembers is the member names of a group as h lists it.
func (h *host) groupMembers(name string) []string {
	var gs []struct {
		Name    string   `json:"name"`
		Members []string `json:"member_names"`
	}
	out, _ := h.run("group", "list", "--json")
	json.Unmarshal([]byte(out), &gs)
	for _, g := range gs {
		if g.Name == name {
			return g.Members
		}
	}
	return nil
}

// blockFrom makes h unreachable over ssh from one machine only.
func (h *host) blockFrom(from *host) {
	os.WriteFile(filepath.Join(h.w.root, "machines", h.name, "block-"+from.name), nil, 0o644)
}

func (h *host) connectedTo(peer string) bool {
	for _, p := range h.status().Peers {
		if p.Name == peer && p.Connected {
			return true
		}
	}
	return false
}

func (h *host) write(rel, s string) {
	p := filepath.Join(h.home, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(s), 0o644)
}

func (h *host) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(h.home, rel))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func eventuallyT(t *testing.T, what string, cond func() bool) {
	t.Helper()
	eventuallyLongT(t, what, 10*time.Second, cond)
}

func eventuallyLongT(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type statusJSON struct {
	Machine string `json:"machine"`
	Fleet   string `json:"fleet"`
	Version string `json:"version"`
	Peers   []struct {
		Name      string `json:"name"`
		Connected bool   `json:"connected"`
		LastError string `json:"last_error"`
	} `json:"peers"`
	Entries []struct {
		Path string `json:"path"`
	} `json:"entries"`
	Problems []struct {
		Key      string `json:"key"`
		Severity string `json:"severity"`
		Message  string `json:"message"`
	} `json:"problems"`
}

func (h *host) status() statusJSON {
	var s statusJSON
	out, code := h.run("status", "--json")
	if code == 0 {
		json.Unmarshal([]byte(out), &s)
	}
	return s
}

func TestE2E_ThreeMachinesSyncThroughTheCLI(t *testing.T) {
	w := newWorld(t)
	a, b, c := w.host("a"), w.host("b"), w.host("c")

	a.must("init", "--name", "a")
	a.startDaemon()
	out := a.add(b)
	for _, want := range []string{"ssh b uname -sm", "~/.local/bin/enigma", "join", "service install", "connected to a"} {
		if !strings.Contains(out, want) {
			t.Fatalf("machine add output missing %q:\n%s", want, out)
		}
	}
	// a cannot reach c over ssh: c's changes must reach a through b, and a
	// must show the missing pair.
	c.blockFrom(a)
	b.add(c) // any member can add a machine

	if b.status().Fleet != a.status().Fleet || a.status().Fleet == "" {
		t.Fatal("machines are not in the same fleet")
	}
	eventuallyT(t, "c linked to b", func() bool { return c.connectedTo("b") })
	eventuallyT(t, "a reports it cannot reach c", func() bool {
		for _, p := range a.status().Peers {
			if p.Name == "c" && !p.Connected && strings.Contains(p.LastError, "refused") {
				return true
			}
		}
		return false
	})

	a.write(".claude/CLAUDE.md", "# rules v1")
	a.must("add", "~/.claude/CLAUDE.md")
	eventuallyT(t, "file on b", func() bool { return b.read(".claude/CLAUDE.md") == "# rules v1" })
	eventuallyT(t, "file on c (relayed by b)", func() bool { return c.read(".claude/CLAUDE.md") == "# rules v1" })

	c.write(".claude/CLAUDE.md", "# rules v2 from c")
	eventuallyT(t, "c's edit on a", func() bool { return a.read(".claude/CLAUDE.md") == "# rules v2 from c" })

	if out := b.must("list"); !strings.Contains(out, "~/.claude/CLAUDE.md") {
		t.Fatalf("list: %s", out)
	}
	out = b.must("status")
	for _, want := range []string{"machine b", "a", "connected", "~/.claude/CLAUDE.md", "\n1 file\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}
	out = c.must("machine", "list")
	for _, want := range []string{"a", "b", "c (this machine)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("machine list missing %q:\n%s", want, out)
		}
	}
}

func TestE2E_FolderEntryAndRemove(t *testing.T) {
	w := newWorld(t)
	a, b := w.host("a"), w.host("b")
	a.must("init", "--name", "a")
	a.startDaemon()
	a.add(b)
	a.write("skills/one.md", "1")
	a.must("add", "--ignore", "*.log", "skills")
	eventuallyT(t, "folder on b", func() bool { return b.read("skills/one.md") == "1" })
	b.write("skills/new.md", "from b")
	b.write("skills/debug.log", "ignored")
	eventuallyT(t, "new file on a", func() bool { return a.read("skills/new.md") == "from b" })
	time.Sleep(300 * time.Millisecond)
	if a.read("skills/debug.log") != "<missing>" {
		t.Fatal("ignored file synced")
	}
	a.must("remove", "skills")
	eventuallyT(t, "entry gone on b", func() bool { return len(b.status().Entries) == 0 })
	if b.read("skills/one.md") != "1" {
		t.Fatal("remove must keep files")
	}
}

func TestE2E_UsefulErrors(t *testing.T) {
	w := newWorld(t)
	a := w.host("a")
	if out, code := a.run("status"); code == 0 || !strings.Contains(out, "not in a fleet") && !strings.Contains(out, "not running") {
		t.Fatalf("status before init: %d %s", code, out)
	}
	if out, code := a.run("machine", "add", "b"); code == 0 || !strings.Contains(out, "not in a fleet") {
		t.Fatalf("machine add before init: %d %s", code, out)
	}
	a.must("init", "--name", "a")
	if out, code := a.run("init"); code == 0 || !strings.Contains(out, "already") {
		t.Fatalf("second init: %d %s", code, out)
	}
	if out, code := a.run("add", "x"); code == 0 || !strings.Contains(out, "not running") {
		t.Fatalf("add without daemon: %d %s", code, out)
	}
	a.startDaemon()
	if out, code := a.run("add", "/etc/hosts"); code == 0 || !strings.Contains(out, "home folder") {
		t.Fatalf("add outside home: %d %s", code, out)
	}
	if out, code := a.run("machine", "add", "nowhere"); code == 0 || !strings.Contains(out, "Could not resolve") {
		t.Fatalf("machine add unknown host: %d %s", code, out)
	}
	if out, code := a.run("machine", "remove", "ghost"); code == 0 || !strings.Contains(out, "no machine named") {
		t.Fatalf("remove unknown machine: %d %s", code, out)
	}
	if out, code := a.run("service", "bogus"); code != 2 || !strings.Contains(out, "install") {
		t.Fatalf("service with a bad action: %d %s", code, out)
	}
	if out, code := a.run("bogus"); code != 2 || !strings.Contains(out, "unknown command") {
		t.Fatalf("unknown command: %d %s", code, out)
	}
	out := a.must("identity")
	var id map[string]string
	if json.Unmarshal([]byte(out), &id) != nil || id["group_id"] == "" || id["name"] != "a" {
		t.Fatalf("identity: %s", out)
	}
}

func TestE2E_MachineAddRefusesAMemberOfAnotherFleet(t *testing.T) {
	w := newWorld(t)
	a, b := w.host("a"), w.host("b")
	a.must("init", "--name", "a")
	a.startDaemon()
	b.must("init", "--name", "b")
	if out, code := a.run("machine", "add", "b"); code == 0 || !strings.Contains(out, "another fleet") {
		t.Fatalf("adding a machine of another fleet: %d %s", code, out)
	}
	if out := a.must("machine", "list"); strings.Contains(out, "b ") {
		t.Fatalf("refused machine recorded:\n%s", out)
	}
}

func TestE2E_MachineRemoveWithUninstall(t *testing.T) {
	w := newWorld(t)
	a, b, c := w.host("a"), w.host("b"), w.host("c")
	a.must("init", "--name", "a")
	a.startDaemon()
	a.add(b)
	a.add(c)
	eventuallyT(t, "b linked to c", func() bool { return b.connectedTo("c") })
	a.write("n/f.md", "kept")
	a.must("add", "n")
	eventuallyT(t, "synced to c", func() bool { return c.read("n/f.md") == "kept" })

	out := a.must("machine", "remove", "c", "--uninstall")
	if !strings.Contains(out, "left the fleet") {
		t.Fatalf("remove output:\n%s", out)
	}
	eventuallyT(t, "c's daemon stopped", func() bool { _, code := c.run("status"); return code != 0 })
	if out, _ := c.run("status"); !strings.Contains(out, "not in a fleet") {
		t.Fatalf("c still in the fleet: %s", out)
	}
	if _, err := os.Stat(filepath.Join(c.home, ".local/bin/enigma")); !os.IsNotExist(err) {
		t.Fatal("--uninstall kept the binary")
	}
	if c.read("n/f.md") != "kept" {
		t.Fatal("leaving must keep the files")
	}
	eventuallyT(t, "b forgot c", func() bool {
		for _, p := range b.status().Peers {
			if p.Name == "c" {
				return false
			}
		}
		return true
	})
}

func TestE2E_MachineRename(t *testing.T) {
	w := newWorld(t)
	a, b := w.host("a"), w.host("b")
	a.must("init", "--name", "a")
	a.startDaemon()
	a.add(b)
	a.must("machine", "rename", "b", "devbox")
	eventuallyT(t, "b sees its new name", func() bool { return strings.Contains(b.must("machine", "list"), "devbox (this machine)") })
	if out, code := a.run("machine", "rename", "devbox", "a"); code == 0 || !strings.Contains(out, "already") {
		t.Fatalf("rename onto a taken name: %d %s", code, out)
	}
}

// One version everywhere (SPEC §11): a machine running a newer build
// upgrades the others over the link.
func TestE2E_NewerBuildUpgradesTheFleet(t *testing.T) {
	dir := t.TempDir()
	oldV, newV := "v0.0.0-dev.20261020100000.aaaaaaa", "v0.0.0-dev.20261020110000.bbbbbbb"
	oldBin, newBin := buildVersion(t, dir, oldV), buildVersion(t, dir, newV)

	w := newWorld(t)
	a, b := w.host("a"), w.host("b")
	a.bin = oldBin
	a.must("init", "--name", "a")
	stopA := a.startDaemon()
	a.add(b) // b gets the old build
	eventuallyT(t, "b runs the old build", func() bool { return b.status().Version == oldV })
	a.write("f.md", "before")
	a.must("add", "f.md")
	eventuallyT(t, "synced", func() bool { return b.read("f.md") == "before" })

	stopA()
	a.bin = newBin // a is upgraded by hand
	a.startDaemon()
	eventuallyT(t, "b upgraded itself to the new build", func() bool { return b.status().Version == newV })
	eventuallyT(t, "linked again", func() bool { return a.connectedTo("b") })
	b.write("f.md", "after the upgrade")
	eventuallyT(t, "sync works on the new build", func() bool { return a.read("f.md") == "after the upgrade" })
}

func TestE2E_DaemonWritesItsOwnLog(t *testing.T) {
	w := newWorld(t)
	a := w.host("a")
	a.must("init", "--name", "a")
	logPath := filepath.Join(a.eh, "enigma.log")
	cmd := exec.Command(bin, "daemon", "--log", logPath)
	cmd.Env, cmd.Dir = a.env(), a.home
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Signal(os.Interrupt); cmd.Wait() }()
	eventuallyT(t, "log written", func() bool {
		b, _ := os.ReadFile(logPath)
		return strings.Contains(string(b), "daemon started")
	})
}

func TestE2E_TrashListRestoreEmpty(t *testing.T) {
	w := newWorld(t)
	a, b := w.host("a"), w.host("b")
	a.must("init", "--name", "a")
	a.startDaemon()
	a.add(b)
	a.write("notes/keep.md", "stays")
	a.write("notes/gone.md", "deleted on a")
	a.must("add", "notes")
	eventuallyT(t, "folder on b", func() bool { return b.read("notes/gone.md") == "deleted on a" })

	os.Remove(filepath.Join(a.home, "notes/gone.md"))
	eventuallyT(t, "delete reaches b", func() bool { return b.read("notes/gone.md") == "<missing>" })

	out := b.must("trash", "list")
	if !strings.Contains(out, "~/notes/gone.md") {
		t.Fatalf("trash list:\n%s", out)
	}
	var items []struct{ ID, Path string }
	if err := json.Unmarshal([]byte(b.must("trash", "list", "--json")), &items); err != nil || len(items) != 1 {
		t.Fatalf("trash list --json: %v %+v", err, items)
	}
	b.must("trash", "restore", items[0].ID)
	eventuallyT(t, "restored file reaches a", func() bool { return a.read("notes/gone.md") == "deleted on a" })

	if _, code := b.run("trash", "restore", items[0].ID); code == 0 {
		t.Fatal("restoring twice must fail")
	}
	os.Remove(filepath.Join(b.home, "notes/keep.md"))
	eventuallyT(t, "delete reaches a", func() bool { return a.read("notes/keep.md") == "<missing>" })
	if out := a.must("trash", "empty"); !strings.Contains(out, "1 file") {
		t.Fatalf("trash empty: %s", out)
	}
	if out := a.must("trash", "list"); !strings.Contains(out, "trash is empty") {
		t.Fatalf("after empty:\n%s", out)
	}
}

// SPEC §8: the limits are a fleet setting, and a folder that grows past
// its file limit says so instead of syncing everything.
func TestE2E_LimitsAreSetForTheWholeFleet(t *testing.T) {
	w := newWorld(t)
	a, b := w.host("a"), w.host("b")
	a.must("init", "--name", "a")
	a.startDaemon()
	a.add(b)
	if out := a.must("limits"); !strings.Contains(out, "10 MB per file") || !strings.Contains(out, "2,000 files per folder") {
		t.Fatalf("limits:\n%s", out)
	}
	a.must("limits", "--files", "3", "--file-size", "1MB")
	eventuallyT(t, "b has the new limits", func() bool {
		out, _ := b.run("limits", "--json")
		return strings.Contains(out, `"max_files":3`) && strings.Contains(out, `"max_file_size":1048576`)
	})
	if out, code := a.run("limits", "--file-size", "65MB"); code != 1 || !strings.Contains(out, "at most 64 MB") {
		t.Fatalf("limits over the ceiling: %d %s", code, out)
	}
	if out, code := a.run("limits", "--files", "lots"); code != 2 {
		t.Fatalf("limits with a bad count: %d %s", code, out)
	}

	a.write("notes/1.md", "one")
	a.must("add", "notes")
	for _, n := range []string{"2", "3", "4"} {
		a.write("notes/"+n+".md", n)
	}
	eventuallyT(t, "status says the folder is over its limit", func() bool {
		out, _ := a.run("status")
		return strings.Contains(out, "over the 3-file limit, 1 new file not synced")
	})
	for _, n := range []string{"1", "2", "3", "4"} {
		a.write("big/"+n, n)
	}
	if out, code := a.run("add", "big"); code == 0 || !strings.Contains(out, "enigma limits --files") {
		t.Fatalf("add over the limit: %d %s", code, out)
	}
}

func TestE2E_ConflictsListsCopiesWithTheirOriginal(t *testing.T) {
	w := newWorld(t)
	a, b := w.host("a"), w.host("b")
	a.must("init", "--name", "a")
	a.startDaemon()
	a.add(b)
	a.write("n/f.md", "base")
	a.must("add", "n")
	eventuallyT(t, "synced", func() bool { return b.read("n/f.md") == "base" })

	b.must("service", "uninstall") // stops b's background daemon
	b.write("n/f.md", "edit on b")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(b.home, "n/f.md"), old, old) // b's edit is the older: it loses
	a.write("n/f.md", "edit on a")
	time.Sleep(300 * time.Millisecond)
	b.must("service", "install")

	eventuallyT(t, "conflict listed on a", func() bool { return strings.Contains(a.must("conflicts"), "~/n/f.md") })
	var got []struct{ Path, Original, Machine string }
	if err := json.Unmarshal([]byte(a.must("conflicts", "--json")), &got); err != nil || len(got) != 1 {
		t.Fatalf("conflicts --json: %v %+v", err, got)
	}
	if got[0].Original != "n/f.md" || got[0].Machine != "b" || !strings.HasPrefix(got[0].Path, "n/f.conflict-b-") {
		t.Fatalf("copy %+v", got[0])
	}
	if c := a.read(got[0].Path); c != "edit on b" {
		t.Fatalf("copy holds %q", c)
	}
	os.Remove(filepath.Join(a.home, got[0].Path)) // resolved
	eventuallyT(t, "resolved everywhere", func() bool { return strings.Contains(b.must("conflicts"), "no conflict copies") })
}

func TestE2E_DoctorShowsTheConnectivityMatrix(t *testing.T) {
	w := newWorld(t)
	a, b, c := w.host("a"), w.host("b"), w.host("c")
	a.must("init", "--name", "a")
	a.startDaemon()
	a.add(b)
	c.blockFrom(a) // a cannot ssh to c; b can
	b.add(c)
	eventuallyT(t, "b linked to c", func() bool { return b.connectedTo("c") })

	var rep struct {
		Machines []struct {
			Name    string          `json:"name"`
			Version string          `json:"version"`
			Error   string          `json:"error"`
			Links   map[string]bool `json:"links"`
		} `json:"machines"`
		Problems []string `json:"problems"`
	}
	out, _ := a.run("doctor", "--json") // stdout and stderr: the JSON, then the exit message
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&rep); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}
	rows := map[string]int{}
	for i, m := range rep.Machines {
		rows[m.Name] = i
	}
	if len(rows) != 3 {
		t.Fatalf("machines %+v", rep.Machines)
	}
	ra, rb, rc := rep.Machines[rows["a"]], rep.Machines[rows["b"]], rep.Machines[rows["c"]]
	if !ra.Links["b"] || ra.Links["c"] || !rb.Links["a"] || !rb.Links["c"] {
		t.Fatalf("links a=%v b=%v", ra.Links, rb.Links)
	}
	if rc.Error == "" || !strings.Contains(rc.Error, "refused") {
		t.Fatalf("c should be unreachable from a: %+v", rc)
	}
	if rb.Version != "dev" {
		t.Fatalf("b's version %q", rb.Version)
	}
	problems := strings.Join(rep.Problems, "\n")
	if !strings.Contains(problems, "a is not linked to c") {
		t.Fatalf("problems:\n%s", problems)
	}

	text, code := a.run("doctor")
	if code == 0 {
		t.Fatal("doctor must exit non-zero when there are problems")
	}
	for _, want := range []string{"FROM \\ TO", "a is not linked to c", "daemon running"} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, text)
		}
	}
}

func TestE2E_ConflictsAcrossTheFleet(t *testing.T) {
	w := newWorld(t)
	a, b := w.host("a"), w.host("b")
	a.must("init", "--name", "a")
	a.startDaemon()
	a.add(b)
	a.write("cfg/rc", "base")
	a.must("add", "cfg/rc") // a single-file entry: its copies sync nowhere
	eventuallyT(t, "synced", func() bool { return b.read("cfg/rc") == "base" })

	b.must("service", "uninstall")
	b.write("cfg/rc", "edit on b")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(b.home, "cfg/rc"), old, old)
	a.write("cfg/rc", "edit on a")
	time.Sleep(300 * time.Millisecond)
	b.must("service", "install")

	type entry struct {
		Path string   `json:"path"`
		On   []string `json:"on"`
	}
	list := func(h *host, args ...string) []entry {
		var got []entry
		json.Unmarshal([]byte(h.must(append([]string{"conflicts", "--json"}, args...)...)), &got)
		return got
	}
	eventuallyT(t, "the copy on b is listed from a", func() bool {
		got := list(a)
		return len(got) == 1 && strings.Contains(strings.Join(got[0].On, ","), "b")
	})
	copyPath := list(a)[0].Path
	os.Remove(filepath.Join(a.home, copyPath))
	os.Remove(filepath.Join(b.home, copyPath))
	eventuallyT(t, "resolved everywhere", func() bool { return len(list(a)) == 0 })
	if got := list(a, "--local"); len(got) != 0 {
		t.Fatalf("--local: %+v", got)
	}
}

func TestE2E_UpdateMovesToTheLatestReleaseAndRestartsTheDaemon(t *testing.T) {
	dir := t.TempDir()
	v1, v2 := buildVersion(t, dir, "v0.1.0"), buildVersion(t, dir, "v0.2.0")
	rel := filepath.Join(dir, "releases")
	url := fileReleaseOf(t, rel, "v0.2.0", v2)

	w := newWorld(t)
	a := w.host("a")
	installed := filepath.Join(a.home, ".local/bin/enigma")
	os.MkdirAll(filepath.Dir(installed), 0o755)
	b, _ := os.ReadFile(v1)
	os.WriteFile(installed, b, 0o755)
	a.bin = installed
	a.extra = []string{"ENIGMA_RELEASES_URL=" + url}
	a.must("init", "--name", "a")
	a.startDaemon()
	if v := a.status().Version; v != "v0.1.0" {
		t.Fatalf("daemon runs %q", v)
	}

	if out := a.must("update", "--check"); !strings.Contains(out, "v0.1.0 -> v0.2.0") {
		t.Fatalf("update --check:\n%s", out)
	}
	if v, _ := exec.Command(installed, "version").Output(); !strings.Contains(string(v), "v0.1.0") {
		t.Fatal("--check changed the binary")
	}
	out := a.must("update")
	if !strings.Contains(out, "updated to v0.2.0") {
		t.Fatalf("update:\n%s", out)
	}
	if v, _ := exec.Command(installed, "version").Output(); !strings.Contains(string(v), "v0.2.0") {
		t.Fatalf("binary not replaced: %s", v)
	}
	eventuallyLongT(t, "daemon restarted on v0.2.0", 30*time.Second, func() bool { return a.status().Version == "v0.2.0" })
	if out := a.must("update"); !strings.Contains(out, "up to date") {
		t.Fatalf("second update:\n%s", out)
	}
}

func TestE2E_TUINeedsATerminal(t *testing.T) {
	w := newWorld(t)
	a := w.host("a")
	if out, code := a.run("tui"); code == 0 || !strings.Contains(out, "needs a terminal") {
		t.Fatalf("tui without a terminal: %d %s", code, out)
	}
	if out, code := a.run(); code != 0 || !strings.Contains(out, "Usage") {
		t.Fatalf("bare enigma in a pipe prints help: %d %s", code, out)
	}
}

func TestE2E_AddWithoutAPathNeedsFzfAndATerminal(t *testing.T) {
	w := newWorld(t)
	a := w.host("a")
	a.must("init", "--name", "a")
	if out, code := a.run("add"); code == 0 || !strings.Contains(out, "give a path, or install fzf to pick one") {
		t.Fatalf("add without a path in a pipe: %d %s", code, out)
	}
}

func TestE2E_D20_StatusAndDoctorShowStandingProblemsFirst(t *testing.T) {
	w := newWorld(t)
	a, b, c := w.host("a"), w.host("b"), w.host("c")
	a.must("init", "--name", "a")
	a.startDaemon()
	a.add(b)
	c.blockFrom(a) // a cannot ssh to c; b can
	b.add(c)
	eventuallyT(t, "b linked to c", func() bool { return b.connectedTo("c") })
	eventuallyT(t, "a's status reports the problem", func() bool {
		for _, p := range a.status().Problems {
			if p.Key == "link:c" && p.Severity == "error" {
				return true
			}
		}
		return false
	})

	out := a.must("status")
	i, j := strings.Index(out, "PROBLEMS"), strings.Index(out, "PEER\t")
	if j < 0 {
		j = strings.Index(out, "PEER ")
	}
	if i < 0 || j < 0 || i > j {
		t.Fatalf("problems must come before the peers:\n%s", out)
	}
	for _, want := range []string{"c link down", "refused", "enigma doctor"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}

	text, code := a.run("doctor")
	if code == 0 || !strings.Contains(text, "problem: c link down") {
		t.Fatalf("doctor (exit %d) must list the standing problem:\n%s", code, text)
	}
	var rep struct {
		Problems []string `json:"problems"`
	}
	jsonOut, _ := a.run("doctor", "--json")
	if err := json.NewDecoder(strings.NewReader(jsonOut)).Decode(&rep); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, jsonOut)
	}
	if len(rep.Problems) == 0 || !strings.HasPrefix(rep.Problems[0], "c link down") {
		t.Fatalf("doctor --json problems: %q", rep.Problems)
	}
}

func TestE2E_D19_GroupsCLI(t *testing.T) {
	w := newWorld(t)
	a, b, c := w.host("a"), w.host("b"), w.host("c")
	a.must("init", "--name", "a")
	a.startDaemon()
	a.add(b)
	a.add(c)
	eventuallyT(t, "everyone linked", func() bool { return a.connectedTo("b") && a.connectedTo("c") && b.connectedTo("c") })

	// main lists every machine.
	out := a.must("group", "list")
	for _, want := range []string{"main", "a", "b", "c"} {
		if !strings.Contains(out, want) {
			t.Fatalf("group list missing %q:\n%s", want, out)
		}
	}
	out = a.must("group", "create", "pair", "a", "b")
	if !strings.Contains(out, "created group pair with a, b") {
		t.Fatalf("create: %s", out)
	}
	eventuallyT(t, "c knows pair", func() bool { out, _ := c.run("group", "list"); return strings.Contains(out, "pair") })
	if out, code := c.run("group", "create", "pair"); code == 0 || !strings.Contains(out, "already exists") {
		t.Fatalf("duplicate name: %d %s", code, out)
	}

	// An entry in pair never reaches c, and c cannot add to pair.
	a.write("secret/x.md", "x")
	out = a.must("add", "--group", "pair", "~/secret")
	if !strings.Contains(out, "syncing ~/secret in group pair") {
		t.Fatalf("add --group: %s", out)
	}
	eventuallyT(t, "b has it", func() bool { return b.read("secret/x.md") == "x" })
	c.write("other/y.md", "y")
	if out, code := c.run("add", "--group", "pair", "~/other"); code == 0 || !strings.Contains(out, "not a member") {
		t.Fatalf("c added to pair: %d %s", code, out)
	}
	if out, _ := c.run("list"); strings.Contains(out, "secret") {
		t.Fatalf("c lists the pair entry:\n%s", out)
	}
	out = a.must("list")
	if !strings.Contains(out, "secret/") || !strings.Contains(out, "pair") {
		t.Fatalf("list must show the group:\n%s", out)
	}

	// Membership: c joins pair and gets the entry; b leaves and freezes.
	a.must("group", "add", "pair", "c")
	eventuallyT(t, "c has it", func() bool { return c.read("secret/x.md") == "x" })
	a.must("group", "remove", "pair", "b")
	eventuallyT(t, "b's entry is frozen", func() bool { out, _ := b.run("list"); return strings.Contains(out, "not synced here") })
	if out, code := b.run("remove", "~/secret"); code == 0 || !strings.Contains(out, "which this machine is not in") {
		t.Fatalf("b removed a frozen entry: %d %s", code, out)
	}

	// Rename, move, delete.
	a.must("group", "rename", "pair", "duo")
	eventuallyT(t, "c sees duo", func() bool { out, _ := c.run("list"); return strings.Contains(out, "duo") })
	if out, code := a.run("group", "delete", "duo"); code == 0 || !strings.Contains(out, "still has 1 entry") {
		t.Fatalf("delete with entries: %d %s", code, out)
	}
	out = a.must("entry", "move", "~/secret", "main")
	if !strings.Contains(out, "moved ~/secret to main") {
		t.Fatalf("entry move: %s", out)
	}
	eventuallyT(t, "b is live again", func() bool { return b.read("secret/x.md") == "x" })
	a.must("group", "delete", "duo")
	eventuallyT(t, "duo is gone everywhere", func() bool { out, _ := c.run("group", "list"); return !strings.Contains(out, "duo") })
	if out, code := a.run("group", "delete", "main"); code == 0 || !strings.Contains(out, "main cannot be deleted") {
		t.Fatalf("delete main: %d %s", code, out)
	}
	var gs []struct {
		Name    string   `json:"name"`
		Members []string `json:"members"`
		Entries int      `json:"entries"`
	}
	if out := a.must("group", "list", "--json"); json.Unmarshal([]byte(out), &gs) != nil || len(gs) != 1 || gs[0].Name != "main" || gs[0].Entries != 1 || len(gs[0].Members) != 3 {
		t.Fatalf("group list --json: %s", out)
	}
}

func TestE2E_D19_GroupMergeShowsThePlanAndMovesEntries(t *testing.T) {
	w := newWorld(t)
	a, b, c := w.host("a"), w.host("b"), w.host("c")
	a.must("init", "--name", "a")
	a.startDaemon()
	a.add(b)
	a.add(c)
	eventuallyT(t, "everyone linked", func() bool { return a.connectedTo("b") && a.connectedTo("c") })
	a.must("group", "create", "pair", "a", "b")
	a.write("p/x.md", "x")
	a.must("add", "--group", "pair", "~/p")
	a.must("group", "remove", "main", "b") // b is in pair only
	eventuallyT(t, "b is out of main", func() bool { return strings.Join(b.groupMembers("main"), ",") == "a,c" })

	// The plan: c gains p; b would lose it, so the merge is refused.
	out, code := a.run("group", "merge", "pair", "main", "--yes")
	if code == 0 || !strings.Contains(out, "c gains ~/p") || !strings.Contains(out, "b would lose ~/p") || !strings.Contains(out, "--add-members") {
		t.Fatalf("merge without --add-members: %d\n%s", code, out)
	}
	out = a.must("group", "merge", "pair", "main", "--yes", "--add-members")
	if !strings.Contains(out, "b joins main") || !strings.Contains(out, "merged pair into main") {
		t.Fatalf("merge: %s", out)
	}
	eventuallyT(t, "c has p", func() bool { return c.read("p/x.md") == "x" })
	eventuallyT(t, "b still has p, live", func() bool {
		out, _ := b.run("list")
		return strings.Contains(out, "p/") && !strings.Contains(out, "not synced here")
	})
	eventuallyT(t, "pair is gone", func() bool { out, _ := a.run("group", "list"); return !strings.Contains(out, "pair") })
	if out, code := a.run("group", "merge", "main", "main", "--yes"); code == 0 {
		t.Fatalf("merging main into itself: %s", out)
	}
}

func TestE2E_D19_MachineAddWithGroups(t *testing.T) {
	w := newWorld(t)
	a, b, c := w.host("a"), w.host("b"), w.host("c")
	a.must("init", "--name", "a")
	a.startDaemon()
	a.must("group", "create", "build")
	out := a.add(b, "--group", "build") // build only: not in main
	if !strings.Contains(out, "in group build, not in main") {
		t.Fatalf("machine add --group: %s", out)
	}
	a.add(c)
	eventuallyT(t, "linked", func() bool { return a.connectedTo("b") && a.connectedTo("c") })
	eventuallyT(t, "b is in build only, c in main", func() bool {
		return strings.Join(a.groupMembers("build"), ",") == "b" && strings.Join(a.groupMembers("main"), ",") == "a,c"
	})
	if out, code := a.run("machine", "add", "d", "--group", "nope"); code == 0 || !strings.Contains(out, "no group") {
		t.Fatalf("unknown group must fail before touching the machine: %d %s", code, out)
	}
}
