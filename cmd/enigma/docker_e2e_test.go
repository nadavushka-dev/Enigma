//go:build docker

// Docker end-to-end suite (SPEC §13): three Linux containers running sshd
// and real daemons, over real ssh. Run with `make e2e` (needs Docker).
// ENIGMA_E2E_MAC=1 adds this Mac as a fourth member, with a throwaway HOME
// so no real files are touched.
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

const (
	image   = "enigma-e2e:latest"
	network = "enigma-e2e-net"
)

type box struct {
	t    *testing.T
	name string
	port string // published sshd port on 127.0.0.1
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func dockerTry(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// sh runs a shell command as user dev inside the box.
func (b *box) sh(cmd string) string {
	b.t.Helper()
	return docker(b.t, "exec", "-u", "dev", "-e", "HOME=/home/dev", b.name, "sh", "-c", cmd)
}

func (b *box) try(cmd string) (string, error) {
	return dockerTry("exec", "-u", "dev", "-e", "HOME=/home/dev", b.name, "sh", "-c", cmd)
}

// startDaemon runs the daemon the way users do: `service install`, which
// falls back to a background daemon (no systemd user session here).
// The binary machine add installed, if any: an upgrade swaps that one
// (the mounted /usr/local/bin/enigma is read-only).
const ownEnigma = `$( [ -x ~/.local/bin/enigma ] && echo ~/.local/bin/enigma || echo enigma )`

func (b *box) startDaemon() {
	b.t.Helper()
	b.sh(ownEnigma + " service install")
}

func (b *box) stopDaemon() {
	b.t.Helper()
	b.sh(ownEnigma + " service uninstall")
}

func (b *box) status() statusJSON {
	var st statusJSON
	out, err := b.try("enigma status --json")
	if err == nil {
		json.Unmarshal([]byte(out), &st)
	}
	return st
}

func (b *box) read(rel string) string {
	out, err := b.try("cat ~/" + rel)
	if err != nil {
		return "<missing>"
	}
	return out
}

func eventuallyLong(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s: %s", d, what)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func TestDocker_ThreeContainers(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if _, err := dockerTry("info"); err != nil {
		t.Skip("docker not running")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())

	// Linux binaries for the containers' architecture: the group starts on
	// "old", then c1 moves to "new" and the others must follow (SPEC §11).
	oldV, newV := "v0.0.0-dev.20261020100000.aaaaaaa", "v0.0.0-dev.20261020110000.bbbbbbb"
	build := func(version string) string {
		out := filepath.Join(root, version, "enigma")
		cmd := exec.Command("go", "build", "-ldflags", "-X github.com/nadavushka-dev/enigma/internal/buildinfo.Version="+version, "-o", out, ".")
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cross build: %v\n%s", err, b)
		}
		return out
	}
	oldBin, newBin := build(oldV), build(newV)
	keys := filepath.Join(root, "keys")
	os.MkdirAll(keys, 0o755)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(keys, "id_ed25519")).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	pub, _ := os.ReadFile(filepath.Join(keys, "id_ed25519.pub"))
	os.WriteFile(filepath.Join(keys, "authorized_keys"), pub, 0o644)

	if _, err := dockerTry("image", "inspect", image); err != nil {
		docker(t, "build", "-q", "-t", image, "../../e2e/docker")
	}
	// Two networks: c3 shares only the second with c2, so c1 cannot reach
	// c3 at all (D1 is assumed, not guaranteed).
	nets := []string{network, network + "-2"}
	for _, n := range nets {
		dockerTry("network", "rm", n)
		docker(t, "network", "create", n)
	}
	var boxes []*box
	t.Cleanup(func() {
		for _, b := range boxes {
			if t.Failed() {
				out, _ := dockerTry("exec", b.name, "sh", "-c", "cat /home/dev/.local/share/enigma/enigma.log")
				t.Logf("--- %s log ---\n%s", b.name, out)
			}
			dockerTry("rm", "-f", b.name)
		}
		for _, n := range nets {
			dockerTry("network", "rm", n)
		}
	})
	for _, name := range []string{"c1", "c2", "c3"} {
		cname := "enigma-e2e-" + name
		dockerTry("rm", "-f", cname)
		net := network
		if name == "c3" {
			net = nets[1]
		}
		docker(t, "run", "-d", "--name", cname, "--hostname", name, "--network", net, "--network-alias", name,
			"-p", "127.0.0.1::22", "-v", oldBin+":/usr/local/bin/enigma:ro", "-v", newBin+":/opt/new/enigma:ro", "-v", keys+":/keys:ro", image)
		docker(t, "exec", cname, "sh", "-c", "cp /keys/id_ed25519 /home/dev/.ssh/id && chown dev:dev /home/dev/.ssh/id && chmod 600 /home/dev/.ssh/id")
		port := docker(t, "port", cname, "22/tcp")
		boxes = append(boxes, &box{t: t, name: cname, port: port[strings.LastIndex(port, ":")+1:]})
	}
	c1, c2, c3 := boxes[0], boxes[1], boxes[2]
	docker(t, "network", "connect", "--alias", "c2", nets[1], c2.name)

	// The group: c1 first, c1 adds c2, c2 adds c3 (c1 cannot reach c3).
	c1.sh("enigma init --name c1")
	c1.startDaemon()
	out := c1.sh("enigma machine add c2")
	for _, want := range []string{"linux/", "ssh -G c2", "connected to c1", "will not survive a reboot"} {
		if !strings.Contains(out, want) {
			t.Fatalf("machine add c2 output missing %q:\n%s", want, out)
		}
	}
	c2.sh("~/.local/bin/enigma machine add c3")
	eventuallyLong(t, "c1 shows it cannot reach c3", 60*time.Second, func() bool {
		for _, p := range c1.status().Peers {
			if p.Name == "c3" && !p.Connected && p.LastError != "" {
				return true
			}
		}
		return false
	})

	// This Mac joins only with ENIGMA_E2E_MAC=1 (the macOS pass); it needs
	// a darwin binary in c1's reach, so it is added from here.
	var mac *host
	if os.Getenv("ENIGMA_E2E_MAC") != "" {
		t.Skip("ENIGMA_E2E_MAC: the Mac member comes with the macOS pass")
	}
	// Compared trimmed: docker exec output loses the final newline.
	same := func(got, want string) bool { return strings.TrimSpace(got) == strings.TrimSpace(want) }
	everyone := func(what string, d time.Duration, want string) {
		t.Helper()
		for _, b := range boxes {
			eventuallyLong(t, b.name+" "+what, d, func() bool { return same(b.read(".claude/CLAUDE.md"), want) })
		}
		if mac != nil {
			eventuallyLong(t, "mac "+what, d, func() bool { return same(mac.read(".claude/CLAUDE.md"), want) })
		}
	}

	// 1. Added on c1, reaches every machine (c3 through c2).
	c1.sh("mkdir -p ~/.claude && printf '# rules from c1' > ~/.claude/CLAUDE.md")
	c1.sh("enigma add ~/.claude/CLAUDE.md")
	everyone("has the file", 30*time.Second, "# rules from c1")

	// 2. Edited on c3 (reachable only through c2), reaches c1.
	c3.sh("printf '# edited on c3' > ~/.claude/CLAUDE.md")
	everyone("has c3's edit", 30*time.Second, "# edited on c3")

	// 3. c3 is cut off (its only link, c2, is down); its offline edit
	//    arrives once c2 is back.
	c2.stopDaemon()
	c3.sh("printf '# offline edit on c3' > ~/.claude/CLAUDE.md")
	time.Sleep(time.Second)
	if c1.read(".claude/CLAUDE.md") == "# offline edit on c3" {
		t.Fatal("c3 should be unreachable while c2 is down")
	}
	c2.startDaemon()
	everyone("has the offline edit", 60*time.Second, "# offline edit on c3")

	// 4. A real network cut: c1 leaves the network, c2 edits meanwhile,
	//    c1 returns and catches up.
	docker(t, "network", "disconnect", network, c1.name)
	c2.sh("printf '# edited while c1 was cut off' > ~/.claude/CLAUDE.md")
	time.Sleep(5 * time.Second)
	if c1.read(".claude/CLAUDE.md") == "# edited while c1 was cut off" {
		t.Fatal("c1 got the edit during the cut: the cut is not real")
	}
	docker(t, "network", "connect", "--alias", "c1", network, c1.name)
	everyone("converges after the cut", 120*time.Second, "# edited while c1 was cut off")

	// 5. Concurrent edits to different lines merge over real links, with
	//    no conflict copy (SPEC §7 step 3).
	c1.sh("printf '1\\n2\\n3\\n4\\n5\\n' > ~/.claude/CLAUDE.md")
	everyone("has the five lines", 30*time.Second, "1\n2\n3\n4\n5\n")
	c3.stopDaemon()
	c3.sh("printf '1 from c3\\n2\\n3\\n4\\n5\\n' > ~/.claude/CLAUDE.md")
	c1.sh("printf '1\\n2\\n3\\n4\\n5 from c1\\n' > ~/.claude/CLAUDE.md")
	for _, b := range []*box{c1, c2} {
		eventuallyLong(t, b.name+" has c1's edit", 30*time.Second, func() bool { return same(b.read(".claude/CLAUDE.md"), "1\n2\n3\n4\n5 from c1\n") })
	}
	c3.startDaemon()
	everyone("has the merge", 60*time.Second, "1 from c3\n2\n3\n4\n5 from c1\n")
	for _, b := range boxes {
		if out := b.sh("enigma conflicts"); !strings.Contains(out, "no conflict copies") {
			t.Fatalf("%s: a clean merge left conflicts:\n%s", b.name, out)
		}
	}

	// 6. One version everywhere: c1 moves to the new build; c2 is upgraded
	//    over its link to c1, then c3 over its link to c2 (c1 cannot reach
	//    c3). Each re-executes itself from the swapped binary.
	for _, b := range boxes {
		if v := b.status().Version; v != oldV {
			t.Fatalf("%s runs %q before the upgrade", b.name, v)
		}
	}
	c1.stopDaemon()
	c1.sh("/opt/new/enigma service install")
	for _, b := range boxes {
		eventuallyLong(t, b.name+" runs the new build", 90*time.Second, func() bool { return b.status().Version == newV })
	}
	c3.sh("printf '# after the upgrade' > ~/.claude/CLAUDE.md")
	everyone("syncs on the new build", 60*time.Second, "# after the upgrade")

	// 7. (ENIGMA_E2E_LONG=1) A long cut: ssh's keepalive (15s x 3) gives up,
	//    the link dies, and the daemons must redial once c1 is back.
	if os.Getenv("ENIGMA_E2E_LONG") == "" {
		return
	}
	docker(t, "network", "disconnect", network, c1.name)
	eventuallyLong(t, "c2 notices the dead link", 120*time.Second, func() bool {
		for _, p := range c2.status().Peers {
			if p.Name == "c1" {
				return !p.Connected
			}
		}
		return false
	})
	c2.sh("printf '# after the link died' > ~/.claude/CLAUDE.md")
	docker(t, "network", "connect", "--alias", "c1", network, c1.name)
	everyone("converges after redial", 180*time.Second, "# after the link died")
}
