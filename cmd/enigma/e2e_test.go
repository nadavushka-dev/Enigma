package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// End-to-end through the real binary: several "machines" on this host,
// each with its own HOME, ENIGMA_HOME and daemon process. A fake ssh maps
// `ssh <alias> <cmd>` onto the machine named <alias>, so join, bridge and
// the daemons' ssh dialing all run for real, minus the network.

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
	w    *world
	name string
	home string
	eh   string // ENIGMA_HOME
}

func newWorld(t *testing.T) *world {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	w := &world{t: t, root: root, ssh: filepath.Join(root, "ssh")}
	// Fake ssh: skip options, take the alias, run the command as that machine.
	script := `#!/bin/sh
while [ $# -gt 0 ]; do case "$1" in -T) shift;; -o) shift 2;; *) break;; esac; done
alias="$1"; shift
dir="` + root + `/machines/$alias"
[ -d "$dir" ] || { echo "ssh: Could not resolve hostname $alias" >&2; exit 255; }
export HOME="$dir/home" ENIGMA_HOME="$dir/enigma" PATH="` + filepath.Dir(bin) + `:$PATH"
exec sh -c "$*"
`
	os.WriteFile(w.ssh, []byte(script), 0o755)
	return w
}

func (w *world) host(name string) *host {
	h := &host{w: w, name: name, home: filepath.Join(w.root, "machines", name, "home"), eh: filepath.Join(w.root, "machines", name, "enigma")}
	os.MkdirAll(h.home, 0o755)
	return h
}

func (h *host) env() []string {
	return append(os.Environ(), "HOME="+h.home, "ENIGMA_HOME="+h.eh, "ENIGMA_SSH="+h.w.ssh)
}

// run executes enigma as this machine and returns combined output and exit code.
func (h *host) run(args ...string) (string, int) {
	cmd := exec.Command(bin, args...)
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

func (h *host) startDaemon() {
	h.w.t.Helper()
	cmd := exec.Command(bin, "daemon")
	cmd.Env, cmd.Dir = h.env(), h.home
	logf, _ := os.Create(filepath.Join(h.w.root, h.name+"-daemon.log"))
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		h.w.t.Fatal(err)
	}
	h.w.t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
		}
		logf.Close()
		if h.w.t.Failed() {
			b, _ := os.ReadFile(logf.Name())
			h.w.t.Logf("--- %s daemon log ---\n%s", h.name, b)
		}
	})
	eventuallyT(h.w.t, h.name+" daemon up", func() bool { _, code := h.run("status"); return code == 0 })
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
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type statusJSON struct {
	Machine string `json:"machine"`
	Group   string `json:"group"`
	Peers   []struct {
		Name      string `json:"name"`
		Connected bool   `json:"connected"`
		LastError string `json:"last_error"`
	} `json:"peers"`
	Entries []struct {
		Path string `json:"path"`
	} `json:"entries"`
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
	if out := b.must("join", "--name", "b", "a"); !strings.Contains(out, "joined") {
		t.Fatalf("join output: %s", out)
	}
	b.startDaemon()
	c.must("join", "--name", "c", "b") // c only knows b: a's changes must reach it through b
	c.startDaemon()

	if b.status().Group != a.status().Group || a.status().Group == "" {
		t.Fatal("machines are not in the same group")
	}
	eventuallyT(t, "c linked to b", func() bool {
		for _, p := range c.status().Peers {
			if p.Name == "b" && p.Connected {
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

	out := b.must("list")
	if !strings.Contains(out, "~/.claude/CLAUDE.md") {
		t.Fatalf("list: %s", out)
	}
	out = b.must("status")
	for _, want := range []string{"machine b", "a", "connected", "~/.claude/CLAUDE.md"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}
}

func TestE2E_FolderEntryAndRemove(t *testing.T) {
	w := newWorld(t)
	a, b := w.host("a"), w.host("b")
	a.must("init", "--name", "a")
	a.startDaemon()
	b.must("join", "--name", "b", "a")
	b.startDaemon()
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
	if out, code := a.run("status"); code == 0 || !strings.Contains(out, "not in a group") && !strings.Contains(out, "not running") {
		t.Fatalf("status before init: %d %s", code, out)
	}
	if out, code := a.run("join", "nowhere"); code == 0 || !strings.Contains(out, "Could not resolve") {
		t.Fatalf("join unknown host: %d %s", code, out)
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

func TestE2E_JoinRefusesAnotherGroup(t *testing.T) {
	w := newWorld(t)
	a, b := w.host("a"), w.host("b")
	a.must("init", "--name", "a")
	b.must("init", "--name", "b")
	if out, code := b.run("join", "a"); code == 0 || !strings.Contains(out, "another group") {
		t.Fatalf("join across groups: %d %s", code, out)
	}
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
	b.must("join", "--name", "b", "a")
	b.startDaemon()
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
