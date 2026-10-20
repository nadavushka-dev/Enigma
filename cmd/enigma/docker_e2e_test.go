//go:build docker

// Docker end-to-end suite (SPEC §13): three Linux containers running sshd
// and real daemons, plus this Mac as a fourth member, all over real ssh.
// Run with `make e2e` (needs Docker). The Mac side uses a throwaway HOME,
// so no real files are touched.
package main

import (
	"fmt"
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

func (b *box) startDaemon() {
	docker(b.t, "exec", "-d", "-u", "dev", "-e", "HOME=/home/dev", b.name, "enigma", "daemon", "-v", "--log", "/home/dev/enigma.log")
	eventuallyLong(b.t, b.name+" daemon up", 20*time.Second, func() bool { _, err := b.try("enigma status"); return err == nil })
}

func (b *box) stopDaemon() {
	b.try("pkill -INT -f 'enigma daemon'")
	eventuallyLong(b.t, b.name+" daemon down", 20*time.Second, func() bool { _, err := b.try("enigma status"); return err != nil })
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

func TestDocker_ThreeContainersAndThisMac(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if _, err := dockerTry("info"); err != nil {
		t.Skip("docker not running")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())

	// Linux binary for the containers' architecture, and a shared ssh key.
	linuxBin := filepath.Join(root, "bin", "enigma")
	build := exec.Command("go", "build", "-o", linuxBin, ".")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cross build: %v\n%s", err, out)
	}
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
	dockerTry("network", "rm", network)
	docker(t, "network", "create", network)
	var boxes []*box
	t.Cleanup(func() {
		for _, b := range boxes {
			if t.Failed() {
				out, _ := dockerTry("exec", b.name, "cat", "/home/dev/enigma.log")
				t.Logf("--- %s log ---\n%s", b.name, out)
			}
			dockerTry("rm", "-f", b.name)
		}
		dockerTry("network", "rm", network)
	})
	for _, name := range []string{"c1", "c2", "c3"} {
		cname := "enigma-e2e-" + name
		dockerTry("rm", "-f", cname)
		docker(t, "run", "-d", "--name", cname, "--hostname", name, "--network", network, "--network-alias", name,
			"-p", "127.0.0.1::22", "-v", linuxBin+":/usr/local/bin/enigma:ro", "-v", keys+":/keys:ro", image)
		docker(t, "exec", cname, "sh", "-c", "cp /keys/id_ed25519 /home/dev/.ssh/id && chown dev:dev /home/dev/.ssh/id && chmod 600 /home/dev/.ssh/id")
		port := docker(t, "port", cname, "22/tcp")
		b := &box{t: t, name: cname, port: port[strings.LastIndex(port, ":")+1:]}
		boxes = append(boxes, b)
	}
	c1, c2, c3 := boxes[0], boxes[1], boxes[2]

	// Containers: c2 joins c1, c3 joins c2 (c3 reaches others only via c2).
	c1.sh("enigma init --name c1")
	c1.startDaemon()
	c2.sh("enigma join --name c2 c1")
	c2.startDaemon()
	c3.sh("enigma join --name c3 c2")
	c3.startDaemon()

	// This Mac: throwaway HOME; ssh through a config that maps c1 to its
	// published port.
	mac := &host{w: &world{t: t, root: root}, name: "mac", home: filepath.Join(root, "mac-home"), eh: filepath.Join(root, "mac-enigma")}
	os.MkdirAll(mac.home, 0o755)
	sshCfg := filepath.Join(root, "ssh_config")
	os.WriteFile(sshCfg, []byte(fmt.Sprintf(`Host c1
  HostName 127.0.0.1
  Port %s
  User dev
  IdentityFile %s
  IdentitiesOnly yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
`, c1.port, filepath.Join(keys, "id_ed25519"))), 0o600)
	os.Chmod(filepath.Join(keys, "id_ed25519"), 0o600)
	wrapper := filepath.Join(root, "ssh")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec ssh -F "+sshCfg+" \"$@\"\n"), 0o755)
	mac.w.ssh = wrapper
	mac.must("join", "--name", "mac", "c1")
	mac.startDaemon()

	// 1. Added on the Mac, reaches every container (c3 through c2).
	mac.write(".claude/CLAUDE.md", "# rules from the mac")
	mac.must("add", "~/.claude/CLAUDE.md")
	for _, b := range boxes {
		eventuallyLong(t, b.name+" has the file", 30*time.Second, func() bool { return b.read(".claude/CLAUDE.md") == "# rules from the mac" })
	}

	// 2. Edited on c3, reaches the Mac.
	c3.sh("printf '# edited on c3' > ~/.claude/CLAUDE.md")
	eventuallyLong(t, "mac has c3's edit", 30*time.Second, func() bool { return mac.read(".claude/CLAUDE.md") == "# edited on c3" })

	// 3. c3 is cut off (its only link, c2, is down); its offline edit
	//    arrives once c2 is back.
	c2.stopDaemon()
	c3.sh("printf '# offline edit on c3' > ~/.claude/CLAUDE.md")
	time.Sleep(time.Second)
	if mac.read(".claude/CLAUDE.md") == "# offline edit on c3" {
		t.Fatal("c3 should be unreachable while c2 is down")
	}
	c2.startDaemon()
	eventuallyLong(t, "offline edit reaches the mac", 60*time.Second, func() bool { return mac.read(".claude/CLAUDE.md") == "# offline edit on c3" })

	// 4. A real network cut: c1 leaves the network (links die by ssh
	//    keepalive), the Mac edits meanwhile, c1 returns and catches up.
	docker(t, "network", "disconnect", network, c1.name)
	mac.write(".claude/CLAUDE.md", "# edited while c1 was cut off")
	time.Sleep(5 * time.Second)
	// The cut must be real: the Mac reaches the others only through c1.
	for _, b := range []*box{c2, c3} {
		if b.read(".claude/CLAUDE.md") == "# edited while c1 was cut off" {
			t.Fatalf("%s got the edit during the cut: the cut is not real", b.name)
		}
	}
	docker(t, "network", "connect", "--alias", "c1", network, c1.name)
	for _, b := range boxes {
		eventuallyLong(t, b.name+" converges after the cut", 120*time.Second, func() bool {
			return b.read(".claude/CLAUDE.md") == "# edited while c1 was cut off"
		})
	}

	// 5. (ENIGMA_E2E_LONG=1) A long cut: ssh's keepalive (15s x 3) gives up,
	//    the link dies, and the daemons must redial once c1 is back.
	if os.Getenv("ENIGMA_E2E_LONG") == "" {
		return
	}
	docker(t, "network", "disconnect", network, c1.name)
	eventuallyLong(t, "mac notices the dead link", 120*time.Second, func() bool {
		for _, p := range mac.status().Peers {
			if p.Name == "c1" {
				return !p.Connected
			}
		}
		return false
	})
	mac.write(".claude/CLAUDE.md", "# after the link died")
	docker(t, "network", "connect", "--alias", "c1", network, c1.name)
	for _, b := range boxes {
		eventuallyLong(t, b.name+" converges after redial", 180*time.Second, func() bool {
			return b.read(".claude/CLAUDE.md") == "# after the link died"
		})
	}
}
