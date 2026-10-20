package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/config"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// node is one simulated machine: its own home, Enigma dirs and daemon.
type node struct {
	t      *testing.T
	name   string
	home   string
	paths  config.Paths
	cancel context.CancelFunc
	done   chan error
	net    *network
}

// network routes "ssh targets" to other nodes' peer sockets, standing in
// for ssh + `enigma bridge`.
type network struct {
	mu    sync.Mutex
	nodes map[string]*node
	down  map[string]bool
}

func (n *network) setDown(target string, down bool) {
	n.mu.Lock()
	n.down[target] = down
	n.mu.Unlock()
}

func (n *network) dial(_ context.Context, p config.Peer) (io.ReadWriteCloser, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.down[p.Target] {
		return nil, errors.New("ssh: connect to host " + p.Target + ": Connection refused")
	}
	target, ok := n.nodes[p.Target]
	if !ok {
		return nil, errors.New("ssh: Could not resolve hostname " + p.Target)
	}
	return net.Dial("unix", target.paths.PeerSocket())
}

func newNetwork() *network { return &network{nodes: map[string]*node{}, down: map[string]bool{}} }

func (n *network) node(t *testing.T, name string) *node {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	nd := &node{t: t, name: name, home: filepath.Join(base, "home"), net: n,
		paths: config.Paths{Config: filepath.Join(base, "e"), Data: filepath.Join(base, "e")}}
	os.MkdirAll(nd.home, 0o755)
	n.mu.Lock()
	n.nodes[name] = nd
	n.mu.Unlock()
	return nd
}

func (nd *node) start() {
	nd.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	nd.cancel, nd.done = cancel, make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		nd.done <- Run(ctx, Options{Paths: nd.paths, Home: nd.home, Log: quiet, Dial: nd.net.dial,
			MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
			Debounce: 30 * time.Millisecond, Ready: func() { close(ready) }})
	}()
	select {
	case <-ready:
	case err := <-nd.done:
		nd.t.Fatalf("%s: daemon exited: %v", nd.name, err)
	case <-time.After(5 * time.Second):
		nd.t.Fatalf("%s: daemon not ready", nd.name)
	}
	nd.t.Cleanup(nd.stop)
}

func (nd *node) stop() {
	if nd.cancel != nil {
		nd.cancel()
		<-nd.done
		nd.cancel = nil
	}
}

func (nd *node) client() *Client { return NewClient(nd.paths) }

func (nd *node) write(rel, s string) {
	p := filepath.Join(nd.home, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(s), 0o644)
}

func (nd *node) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(nd.home, rel))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// pair sets up a (first machine) and b (joined to a's group, dialing a).
func pair(t *testing.T) (*network, *node, *node) {
	n := newNetwork()
	a, b := n.node(t, "a"), n.node(t, "b")
	ida, err := config.Init(a.paths, "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.Join(b.paths, ida.GroupID, "b"); err != nil {
		t.Fatal(err)
	}
	config.AddPeer(b.paths, config.Peer{Name: "a", Target: "a"})
	a.start()
	b.start()
	return n, a, b
}

func connected(nd *node, peer string) func() bool {
	return func() bool {
		st, err := nd.client().Status()
		if err != nil {
			return false
		}
		for _, p := range st.Peers {
			if p.Name == peer && p.Connected {
				return true
			}
		}
		return false
	}
}

func TestDaemonsLinkAndSync(t *testing.T) {
	_, a, b := pair(t)
	eventually(t, "b linked to a", connected(b, "a"))
	eventually(t, "a sees b", connected(a, "b"))
	a.write(".claude/CLAUDE.md", "# shared")
	if err := a.client().Add(filepath.Join(a.home, ".claude/CLAUDE.md"), nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "file on b", func() bool { return b.read(".claude/CLAUDE.md") == "# shared" })
	b.write(".claude/CLAUDE.md", "# edited on b")
	eventually(t, "edit on a", func() bool { return a.read(".claude/CLAUDE.md") == "# edited on b" })
}

func TestStatusReportsIdentityEntriesAndPeers(t *testing.T) {
	_, a, b := pair(t)
	eventually(t, "linked", connected(b, "a"))
	a.write("notes/x.md", "x")
	a.client().Add(filepath.Join(a.home, "notes"), []string{"*.log"})
	eventually(t, "entry on b", func() bool {
		st, _ := b.client().Status()
		return len(st.Entries) == 1 && st.Entries[0].Path == "notes" && st.Entries[0].Dir
	})
	st, err := b.client().Status()
	if err != nil {
		t.Fatal(err)
	}
	ida, _ := config.LoadIdentity(a.paths)
	if st.Machine != "b" || st.Group != ida.GroupID {
		t.Fatalf("identity in status: %+v", st)
	}
}

func TestReconnectAndCatchUpAfterPeerRestart(t *testing.T) {
	_, a, b := pair(t)
	eventually(t, "linked", connected(b, "a"))
	a.write("f.md", "v1")
	a.client().Add(filepath.Join(a.home, "f.md"), nil)
	eventually(t, "synced", func() bool { return b.read("f.md") == "v1" })
	a.stop()
	b.write("f.md", "edited while a was down")
	time.Sleep(200 * time.Millisecond)
	a.start()
	eventually(t, "b reconnects and a catches up", func() bool { return a.read("f.md") == "edited while a was down" })
}

func TestDialErrorsShowInStatus(t *testing.T) {
	n, _, b := pair(t)
	n.setDown("a", true)
	config.AddPeer(b.paths, config.Peer{Name: "ghost", Target: "ghost"})
	b.client().Reload()
	eventually(t, "ghost error visible", func() bool {
		st, _ := b.client().Status()
		for _, p := range st.Peers {
			if p.Name == "ghost" && !p.Connected && strings.Contains(p.LastError, "Could not resolve") {
				return true
			}
		}
		return false
	})
}

func TestReloadPicksUpNewPeers(t *testing.T) {
	n := newNetwork()
	a, b := n.node(t, "a"), n.node(t, "b")
	ida, _ := config.Init(a.paths, "a")
	config.Join(b.paths, ida.GroupID, "b")
	a.start()
	b.start() // no peers yet
	time.Sleep(100 * time.Millisecond)
	if connected(b, "a")() {
		t.Fatal("linked without a peer entry")
	}
	config.AddPeer(b.paths, config.Peer{Name: "a", Target: "a"})
	if err := b.client().Reload(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "linked after reload", connected(b, "a"))
}

func TestSecondDaemonIsRefused(t *testing.T) {
	n := newNetwork()
	a := n.node(t, "a")
	config.Init(a.paths, "a")
	a.start()
	err := Run(context.Background(), Options{Paths: a.paths, Home: a.home, Log: quiet, Dial: n.dial})
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("err = %v", err)
	}
}

func TestDaemonRequiresInit(t *testing.T) {
	n := newNetwork()
	a := n.node(t, "a")
	err := Run(context.Background(), Options{Paths: a.paths, Home: a.home, Log: quiet, Dial: n.dial})
	if !errors.Is(err, config.ErrNotInitialized) {
		t.Fatalf("err = %v", err)
	}
}

func TestAddErrorsReachTheClient(t *testing.T) {
	_, a, _ := pair(t)
	err := a.client().Add(filepath.Join(a.home, "does-not-exist"), nil)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientWithoutDaemonExplains(t *testing.T) {
	_, err := NewClient(config.Paths{Data: t.TempDir()}).Status()
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("err = %v", err)
	}
}
