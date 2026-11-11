//go:build docker

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// macHome moves dev's home to /Users/dev, as on macOS: ssh, the daemon and
// the CLI then all see HOME=/Users/dev on this box.
func (b *box) macHome() {
	b.t.Helper()
	docker(b.t, "exec", b.name, "sh", "-c", "mkdir -p /Users && mv /home/dev /Users/dev && usermod -d /Users/dev dev")
	b.home = "/Users/dev"
}

// barrier waits until a marker written on from in ~/notes (a synced
// entry) reaches to: one ordered link, so everything from sent earlier
// has been handled by to.
func barrier(t *testing.T, from, to *box, n int) {
	t.Helper()
	mark := fmt.Sprintf("notes/barrier-%s-%d", from.name, n)
	from.sh("printf x > ~/" + mark)
	eventuallyLong(t, "barrier "+mark, 60*time.Second, func() bool { return to.read(mark) == "x" })
}

func (b *box) readlink(rel string) string {
	out, err := b.try("readlink ~/" + rel)
	if err != nil {
		return "<no link>"
	}
	return out
}

// linkState is the state of the link at rel in b's status (D23), "" when
// it is not listed.
func (b *box) linkState(rel string) string {
	out, err := b.try("enigma status --json")
	if err != nil {
		return ""
	}
	var st struct {
		Entries []struct {
			Links []struct{ Path, State string }
		} `json:"entries"`
	}
	json.Unmarshal([]byte(out), &st)
	for _, e := range st.Entries {
		for _, l := range e.Links {
			if l.Path == rel {
				return l.State
			}
		}
	}
	return ""
}

// TestDocker_LinkReplication (SPEC §8.1, D23): a Linux home (/home/dev) and
// a macOS-style home (/Users/dev) over real ssh. A skills folder with links
// on, linking into a repo's skills folder synced in the same group: links
// are written under each machine's own home, a link waits for its target
// through a network cut and appears on catch-up, retargets and deletes
// spread, and a link never replaces a real folder.
func TestDocker_LinkReplication(t *testing.T) {
	needDocker(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	bin := filepath.Join(root, "enigma")
	cmd := exec.Command("go", "build", "-ldflags", "-X github.com/nadavushka-dev/enigma/internal/buildinfo.Version=v0.0.0-dev.20261110100000.ccccccc", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross build: %v\n%s", err, b)
	}
	keys := sshKeys(t, root)
	const prefix = "enigma-e2e-ln-"
	boxes := startBoxes(t, prefix, keys, bin, bin, false)
	lin, mac := boxes[0], boxes[1] // the third box stays out of the fleet
	mac.macHome()

	// The layout on the Linux box: a repo's skills, and a flat skills
	// folder linking into it, one link absolute, one relative.
	lin.sh("mkdir -p ~/work/repo/skills/foo ~/work/repo/skills/bar ~/.agents/skills" +
		" && printf 'foo skill' > ~/work/repo/skills/foo/SKILL.md" +
		" && printf 'bar skill' > ~/work/repo/skills/bar/SKILL.md" +
		" && printf 'plain' > ~/.agents/skills/plain.md" +
		" && ln -s /home/dev/work/repo/skills/foo ~/.agents/skills/foo" +
		" && ln -s ../../work/repo/skills/bar ~/.agents/skills/bar" +
		" && ln -s /home/dev/work/repo/skills/foo ~/.agents/skills/real" +
		// The real fleet's shape: a per-skill link to a plugin folder no
		// entry syncs.
		" && mkdir -p ~/.claude/plugins/m/squad/skills/tkt-work" +
		" && printf 'plugin' > ~/.claude/plugins/m/squad/skills/tkt-work/SKILL.md" +
		" && ln -s /home/dev/.claude/plugins/m/squad/skills/tkt-work ~/.agents/skills/tkt-work" +
		// A folder synced with links off, for the restore check.
		" && mkdir -p ~/notes/sub && printf 'a1' > ~/notes/sub/a.md && printf 'b1' > ~/notes/sub/b.md && printf 'n' > ~/notes/n.md")
	// The Mac box already has a real folder where the Linux box has a link.
	mac.sh("mkdir -p ~/.agents/skills/real && printf 'mac own' > ~/.agents/skills/real/SKILL.md")

	lin.sh("enigma init --name lin")
	lin.startDaemon()
	lin.sh("enigma machine add " + strings.TrimPrefix(mac.name, prefix))
	lin.sh("enigma add ~/work/repo/skills")
	lin.sh("enigma add ~/notes")
	lin.sh("enigma add --links --on-existing keep-both ~/.agents/skills")

	// 1. Each machine writes the link under its own home.
	eventuallyLong(t, "mac has the target", 60*time.Second, func() bool { return mac.read("work/repo/skills/foo/SKILL.md") == "foo skill" })
	eventuallyLong(t, "mac has the absolute link, under /Users/dev", 60*time.Second, func() bool {
		return mac.readlink(".agents/skills/foo") == "/Users/dev/work/repo/skills/foo"
	})
	eventuallyLong(t, "mac has the relative link verbatim", 30*time.Second, func() bool {
		return mac.readlink(".agents/skills/bar") == "../../work/repo/skills/bar"
	})
	if got := mac.read(".agents/skills/foo/SKILL.md"); got != "foo skill" {
		t.Fatalf("mac's link does not reach the skill: %q", got)
	}
	if got := lin.readlink(".agents/skills/foo"); got != "/home/dev/work/repo/skills/foo" {
		t.Fatalf("lin's own link changed: %q", got)
	}

	// 2. A link never replaces a real folder: blocked on the Mac box, and
	//    nothing written through the link on the Linux box.
	eventuallyLong(t, "mac reports the link blocked", 30*time.Second, func() bool { return mac.linkState(".agents/skills/real") == "blocked" })
	if got := mac.read(".agents/skills/real/SKILL.md"); got != "mac own" {
		t.Fatalf("mac's folder was clobbered: %q", got)
	}
	if out := mac.sh("enigma status"); !strings.Contains(out, "links on") || !strings.Contains(out, "blocked") {
		t.Fatalf("mac status does not show the links:\n%s", out)
	}
	barrier(t, mac, lin, 1)
	if got := lin.read("work/repo/skills/foo/SKILL.md"); got != "foo skill" {
		t.Fatalf("lin's repo file changed through its link: %q", got)
	}
	if out := lin.sh("ls ~/work/repo/skills/foo"); strings.TrimSpace(out) != "SKILL.md" {
		t.Fatalf("lin's repo folder changed through its link: %q", out)
	}

	// 3. A link dangling where it is made is not sent; a link to a plugin
	//    folder no entry syncs is not either. Both are reported.
	lin.sh("ln -s /home/dev/work/repo/skills/later ~/.agents/skills/later")
	eventuallyLong(t, "lin reports the dangling link skipped", 30*time.Second, func() bool { return lin.linkState(".agents/skills/later") == "skipped" })
	eventuallyLong(t, "lin reports the plugin link skipped", 30*time.Second, func() bool { return lin.linkState(".agents/skills/tkt-work") == "skipped" })
	barrier(t, lin, mac, 2)
	for _, rel := range []string{".agents/skills/later", ".agents/skills/tkt-work"} {
		if got := mac.readlink(rel); got != "<no link>" || mac.linkState(rel) != "" {
			t.Fatalf("%s reached mac: %q", rel, got)
		}
	}
	if got := lin.read(".claude/plugins/m/squad/skills/tkt-work/SKILL.md"); got != "plugin" {
		t.Fatalf("the plugin folder changed: %q", got)
	}

	// 4. The target arrives while the Mac box is cut off; on catch-up the
	//    target syncs and the link is created. A retarget made during the
	//    cut arrives too.
	net := prefix + "net"
	docker(t, "network", "disconnect", net, mac.name)
	lin.sh("mkdir -p ~/work/repo/skills/later && printf 'later skill' > ~/work/repo/skills/later/SKILL.md")
	lin.sh("ln -sfn /home/dev/work/repo/skills/bar ~/.agents/skills/foo")
	time.Sleep(5 * time.Second)
	if mac.readlink(".agents/skills/later") != "<no link>" || mac.read("work/repo/skills/later/SKILL.md") != "<missing>" {
		t.Fatal("mac got changes during the cut: the cut is not real")
	}
	docker(t, "network", "connect", "--alias", strings.TrimPrefix(mac.name, prefix), net, mac.name)
	eventuallyLong(t, "mac creates the pending link on catch-up", 120*time.Second, func() bool {
		return mac.readlink(".agents/skills/later") == "/Users/dev/work/repo/skills/later"
	})
	if got := mac.read(".agents/skills/later/SKILL.md"); got != "later skill" {
		t.Fatalf("the caught-up link does not reach its skill: %q", got)
	}
	eventuallyLong(t, "mac has the retarget", 60*time.Second, func() bool {
		return mac.readlink(".agents/skills/foo") == "/Users/dev/work/repo/skills/bar"
	})

	// 5. Retarget from the Mac box spreads back, written under /home/dev.
	mac.sh("ln -sfn /Users/dev/work/repo/skills/later ~/.agents/skills/foo")
	eventuallyLong(t, "lin has the Mac box's retarget", 60*time.Second, func() bool {
		return lin.readlink(".agents/skills/foo") == "/home/dev/work/repo/skills/later"
	})

	// 6. A delete moves the link itself to the trash; its target stays.
	lin.sh("rm ~/.agents/skills/bar")
	eventuallyLong(t, "mac deleted the link", 60*time.Second, func() bool { return mac.readlink(".agents/skills/bar") == "<no link>" })
	if got := mac.read("work/repo/skills/bar/SKILL.md"); got != "bar skill" {
		t.Fatalf("deleting the link touched its target: %q", got)
	}
	if out := mac.sh("enigma trash list"); !strings.Contains(out, ".agents/skills/bar") {
		t.Fatalf("the link is not in mac's trash:\n%s", out)
	}
	if got := mac.read(".agents/skills/real/SKILL.md"); got != "mac own" {
		t.Fatalf("mac's folder was clobbered later: %q", got)
	}

	// 7. Restore, never delete (links off): the Mac box moves a synced
	//    folder into a repo and leaves a symlink; the Linux box keeps its
	//    files, and its edit is not written through the link. When the link
	//    goes, the files come back into the real folder; nothing is deleted.
	eventuallyLong(t, "mac has notes", 60*time.Second, func() bool {
		return mac.read("notes/sub/a.md") == "a1" && mac.read("notes/sub/b.md") == "b1"
	})
	mac.sh("mkdir -p ~/work/repo-notes && mv ~/notes/sub ~/work/repo-notes/sub && ln -s /Users/dev/work/repo-notes/sub ~/notes/sub")
	barrier(t, mac, lin, 3)
	lin.sh("printf 'a2 from lin' > ~/notes/sub/a.md")
	barrier(t, lin, mac, 4)
	if got := lin.read("notes/sub/a.md"); got != "a2 from lin" {
		t.Fatalf("lin's file after mac's link: %q", got)
	}
	if got := mac.read("work/repo-notes/sub/a.md"); got != "a1" {
		t.Fatalf("written through mac's link: %q", got)
	}
	mac.sh("rm ~/notes/sub")
	eventuallyLong(t, "mac gets the files back", 60*time.Second, func() bool {
		return mac.read("notes/sub/a.md") == "a2 from lin" && mac.read("notes/sub/b.md") == "b1"
	})
	barrier(t, mac, lin, 5)
	if got := lin.read("notes/sub/a.md"); got != "a2 from lin" {
		t.Fatalf("removing the link deleted lin's file: %q", got)
	}
	if got := lin.read("notes/sub/b.md"); got != "b1" { // untouched by lin: only restore keeps it
		t.Fatalf("removing the link deleted lin's file: %q", got)
	}
	if out := lin.sh("enigma trash list"); strings.Contains(out, "notes/sub") {
		t.Fatalf("lin trashed the folder's files:\n%s", out)
	}
	if got := mac.read("work/repo-notes/sub/a.md"); got != "a1" {
		t.Fatalf("the repo copy changed: %q", got)
	}
}
