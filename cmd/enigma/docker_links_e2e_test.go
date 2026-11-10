//go:build docker

package main

import (
	"encoding/json"
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
		" && ln -s /home/dev/work/repo/skills/foo ~/.agents/skills/real")
	// The Mac box already has a real folder where the Linux box has a link.
	mac.sh("mkdir -p ~/.agents/skills/real && printf 'mac own' > ~/.agents/skills/real/SKILL.md")

	lin.sh("enigma init --name lin")
	lin.startDaemon()
	lin.sh("enigma machine add " + strings.TrimPrefix(mac.name, prefix))
	lin.sh("enigma add ~/work/repo/skills")
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
	time.Sleep(2 * time.Second)
	if got := lin.read("work/repo/skills/foo/SKILL.md"); got != "foo skill" {
		t.Fatalf("lin's repo file changed through its link: %q", got)
	}
	if out := lin.sh("ls ~/work/repo/skills/foo"); strings.TrimSpace(out) != "SKILL.md" {
		t.Fatalf("lin's repo folder changed through its link: %q", out)
	}

	// 3. A link to a target that is not there yet waits on the Mac box.
	lin.sh("ln -s /home/dev/work/repo/skills/later ~/.agents/skills/later")
	eventuallyLong(t, "mac holds the link as pending", 30*time.Second, func() bool { return mac.linkState(".agents/skills/later") == "pending" })
	if got := mac.readlink(".agents/skills/later"); got != "<no link>" {
		t.Fatalf("a dangling link was created: %q", got)
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
}
