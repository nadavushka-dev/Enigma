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
	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/problems"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// logBuffer collects one node's debug log; it is printed if the test fails.
type logBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// node is one simulated machine: its own home, Enigma dirs and daemon.
type node struct {
	t       *testing.T
	name    string
	home    string
	paths   config.Paths
	cancel  context.CancelFunc
	done    chan error
	net     *network
	aliases map[string]bool // ssh aliases this machine's ssh config has
	mods    []func(*Options)
	log     *logBuffer
}

// network routes ssh targets to nodes' peer sockets, standing in for ssh +
// `enigma bridge`, and records every dial.
type network struct {
	mu       sync.Mutex
	routes   map[string]*node
	down     map[string]bool
	attempts []string // "from->target"
}

func newNetwork() *network { return &network{routes: map[string]*node{}, down: map[string]bool{}} }

func (n *network) setDown(target string, down bool) {
	n.mu.Lock()
	n.down[target] = down
	n.mu.Unlock()
}

func (n *network) route(target string, to *node) {
	n.mu.Lock()
	n.routes[target] = to
	n.mu.Unlock()
}

func (n *network) tried() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.attempts...)
}

func (n *network) dialer(from string) func(context.Context, string, string) (io.ReadWriteCloser, error) {
	return func(_ context.Context, target, _ string) (io.ReadWriteCloser, error) {
		n.mu.Lock()
		defer n.mu.Unlock()
		n.attempts = append(n.attempts, from+"->"+target)
		if n.down[target] {
			return nil, errors.New("ssh: connect to host " + target + ": Connection refused")
		}
		to, ok := n.routes[target]
		if !ok {
			return nil, errors.New("ssh: Could not resolve hostname " + target)
		}
		return net.Dial("unix", to.paths.PeerSocket())
	}
}

// dial is the dialer of a machine outside every test's network bookkeeping.
func (n *network) dial(ctx context.Context, target, cmd string) (io.ReadWriteCloser, error) {
	return n.dialer("?")(ctx, target, cmd)
}

func (n *network) node(t *testing.T, name string, mods ...func(*Options)) *node {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	nd := &node{t: t, name: name, home: filepath.Join(base, "home"), net: n, aliases: map[string]bool{}, mods: mods,
		paths: config.Paths{Config: filepath.Join(base, "e"), Data: filepath.Join(base, "e")}, log: &logBuffer{}}
	os.MkdirAll(nd.home, 0o755)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("--- %s log ---\n%s", name, nd.log)
		}
	})
	return nd
}

func (nd *node) hasAlias(a string) bool {
	nd.net.mu.Lock()
	defer nd.net.mu.Unlock()
	return nd.aliases[a]
}

func (nd *node) options(ready func()) Options {
	logger := slog.New(slog.NewTextHandler(nd.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	o := Options{Paths: nd.paths, Home: nd.home, Log: logger, Dial: nd.net.dialer(nd.name), HasAlias: nd.hasAlias,
		MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
		Debounce: 30 * time.Millisecond, Ready: ready}
	for _, f := range nd.mods {
		f(&o)
	}
	return o
}

func (nd *node) start() {
	nd.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	nd.cancel, nd.done = cancel, make(chan error, 1)
	ready := make(chan struct{})
	go func() { nd.done <- Run(ctx, nd.options(func() { close(ready) })) }()
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

func (nd *node) id() config.Identity {
	id, err := config.LoadIdentity(nd.paths)
	if err != nil {
		nd.t.Fatal(err)
	}
	return id
}

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

// joinTo makes b a member of a's fleet the way `machine add` does: b joins
// with its own record, then a records it. It returns b's record.
func joinTo(t *testing.T, a, b *node, rec proto.MachineRecord) proto.MachineRecord {
	t.Helper()
	ida := a.id()
	rec.ID = config.NewID()
	if rec.Name == "" {
		rec.Name = b.name
	}
	if err := config.Join(b.paths, config.Identity{MachineID: rec.ID, FleetID: ida.FleetID, Name: rec.Name}, []proto.MachineRecord{rec}); err != nil {
		t.Fatal(err)
	}
	return rec
}

// pair sets up a (first machine) and b, added by a: a dials b at "ssh://b".
func pair(t *testing.T) (*network, *node, *node) {
	n := newNetwork()
	a, b := n.node(t, "a"), n.node(t, "b")
	if _, err := config.Init(a.paths, "a"); err != nil {
		t.Fatal(err)
	}
	rec := joinTo(t, a, b, proto.MachineRecord{Alias: "b", Target: "ssh://b"})
	n.route("ssh://b", b)
	a.start()
	b.start()
	if err := a.client().SetMachine(rec); err != nil {
		t.Fatal(err)
	}
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

func peerReport(nd *node, peer string) PeerReport {
	st, _ := nd.client().Status()
	for _, p := range st.Peers {
		if p.Name == peer {
			return p
		}
	}
	return PeerReport{}
}

func TestDaemonsLinkAndSync(t *testing.T) {
	_, a, b := pair(t)
	eventually(t, "a linked to b", connected(a, "b"))
	eventually(t, "b sees a", connected(b, "a"))
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
	if st.Machine != "b" || st.Fleet != ida.FleetID {
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
	_, _, b := pair(t)
	b.client().SetMachine(proto.MachineRecord{ID: config.NewID(), Name: "ghost", Target: "ghost"})
	eventually(t, "ghost error visible", func() bool {
		p := peerReport(b, "ghost")
		return !p.Connected && strings.Contains(p.LastError, "Could not resolve")
	})
}

func TestNewMachineRecordIsDialed(t *testing.T) {
	n := newNetwork()
	a, b := n.node(t, "a"), n.node(t, "b")
	config.Init(a.paths, "a")
	rec := joinTo(t, a, b, proto.MachineRecord{Target: "ssh://b"})
	n.route("ssh://b", b)
	a.start()
	b.start()
	time.Sleep(100 * time.Millisecond)
	if connected(a, "b")() {
		t.Fatal("linked before a knew b")
	}
	a.client().SetMachine(rec)
	eventually(t, "linked once recorded", connected(a, "b"))
	eventually(t, "b learned a's record", func() bool { return peerReport(b, "a").ID == a.id().MachineID })
}

func TestTargetResolutionOrder(t *testing.T) {
	n := newNetwork()
	a, b := n.node(t, "a"), n.node(t, "b")
	config.Init(a.paths, "a")
	rec := joinTo(t, a, b, proto.MachineRecord{Alias: "b-alias", Target: "ssh://b-target"})
	for _, tg := range []string{"b-override", "b-alias", "ssh://b-target"} {
		n.route(tg, b)
	}
	b.start()
	firstTry := func() string {
		for _, x := range n.tried() {
			if strings.HasPrefix(x, "a->") {
				return strings.TrimPrefix(x, "a->")
			}
		}
		return ""
	}
	run := func(setup func(), want string) {
		t.Helper()
		n.mu.Lock()
		n.attempts = nil
		n.mu.Unlock()
		setup()
		a.start()
		a.client().SetMachine(rec)
		eventually(t, "linked via "+want, connected(a, "b"))
		if got := firstTry(); got != want {
			t.Fatalf("first target tried %q, want %q", got, want)
		}
		if p := peerReport(a, "b"); p.Target != want {
			t.Fatalf("status target %q", p.Target)
		}
		a.stop()
	}
	run(func() {}, "ssh://b-target")                                                   // a lacks the alias: the captured target
	run(func() { n.mu.Lock(); a.aliases["b-alias"] = true; n.mu.Unlock() }, "b-alias") // a has the alias
	run(func() { config.SetAlias(a.paths, "b", "b-override") }, "b-override")          // a local override wins
}

func TestUnreachableTargetFallsBackToTheNext(t *testing.T) {
	n := newNetwork()
	a, b := n.node(t, "a"), n.node(t, "b")
	config.Init(a.paths, "a")
	a.aliases["b-alias"] = true
	rec := joinTo(t, a, b, proto.MachineRecord{Alias: "b-alias", Target: "ssh://b-target"})
	n.route("ssh://b-target", b)
	n.setDown("b-alias", true)
	a.start()
	b.start()
	a.client().SetMachine(rec)
	eventually(t, "linked via the captured target", connected(a, "b"))
	if p := peerReport(a, "b"); p.Target != "ssh://b-target" {
		t.Fatalf("target in use %q", p.Target)
	}
}

func TestRemovedMachineIsNotDialed(t *testing.T) {
	n, a, _ := pair(t)
	eventually(t, "linked", connected(a, "b"))
	rec := peerReport(a, "b")
	recs, _ := a.client().Machines()
	for _, r := range recs {
		if r.ID == rec.ID {
			r.Removed = true
			a.client().SetMachine(r)
		}
	}
	eventually(t, "link dropped", func() bool { return !connected(a, "b")() })
	time.Sleep(100 * time.Millisecond)
	before := len(n.tried())
	time.Sleep(300 * time.Millisecond)
	if after := len(n.tried()); after != before {
		t.Fatalf("still dialing a removed machine: %v", n.tried()[before:])
	}
	if p := peerReport(a, "b"); p.Name != "" {
		t.Fatalf("removed machine still listed: %+v", p)
	}
}

// fakeBinary is an "enigma" whose `version` prints v.
func fakeBinary(t *testing.T, v string) string {
	p := filepath.Join(t.TempDir(), "bin", "enigma")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("#!/bin/sh\necho enigma "+v+" test/platform\n"), 0o755)
	return p
}

func TestUpgradeSwapsTheBinaryAndRestarts(t *testing.T) {
	newBin, oldBin := fakeBinary(t, "v1.1.0"), fakeBinary(t, "v1.0.0")
	n := newNetwork()
	a := n.node(t, "a", func(o *Options) { o.Version, o.Bin = "v1.1.0", newBin })
	b := n.node(t, "b", func(o *Options) { o.Version, o.Bin = "v1.0.0", oldBin })
	config.Init(a.paths, "a")
	rec := joinTo(t, a, b, proto.MachineRecord{Target: "ssh://b"})
	n.route("ssh://b", b)
	a.start()
	b.start()
	a.client().SetMachine(rec)
	select {
	case err := <-b.done:
		if !errors.Is(err, ErrRestart) {
			t.Fatalf("b exited with %v, want ErrRestart", err)
		}
		b.cancel = nil
	case <-time.After(30 * time.Second): // macOS scans a new executable on its first run: seconds under load
		t.Fatal("b was not upgraded")
	}
	got, _ := os.ReadFile(oldBin)
	want, _ := os.ReadFile(newBin)
	if string(got) != string(want) {
		t.Fatalf("b's binary is %q", got)
	}
}

func TestUpgradeRefusesABinaryOfAnotherVersion(t *testing.T) {
	liar := fakeBinary(t, "v0.1.0") // claims v1.1.0 in the handshake, is v0.1.0
	oldBin := fakeBinary(t, "v1.0.0")
	n := newNetwork()
	a := n.node(t, "a", func(o *Options) { o.Version, o.Bin = "v1.1.0", liar })
	b := n.node(t, "b", func(o *Options) { o.Version, o.Bin = "v1.0.0", oldBin })
	config.Init(a.paths, "a")
	rec := joinTo(t, a, b, proto.MachineRecord{Target: "ssh://b"})
	n.route("ssh://b", b)
	a.start()
	b.start()
	a.client().SetMachine(rec)
	time.Sleep(500 * time.Millisecond)
	select {
	case err := <-b.done:
		t.Fatalf("b exited: %v", err)
	default:
	}
	if got, _ := os.ReadFile(oldBin); !strings.Contains(string(got), "v1.0.0") {
		t.Fatalf("b's binary replaced: %q", got)
	}
}

func TestSecondDaemonIsRefused(t *testing.T) {
	n := newNetwork()
	a := n.node(t, "a")
	config.Init(a.paths, "a")
	a.start()
	err := Run(context.Background(), Options{Paths: a.paths, Home: a.home, Log: quiet, Dial: n.dial, HasAlias: a.hasAlias})
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("err = %v", err)
	}
}

func TestDaemonRequiresInit(t *testing.T) {
	n := newNetwork()
	a := n.node(t, "a")
	err := Run(context.Background(), Options{Paths: a.paths, Home: a.home, Log: quiet, Dial: n.dial, HasAlias: a.hasAlias})
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

func TestEventsLongPoll(t *testing.T) {
	_, a, b := pair(t)
	eventually(t, "linked", connected(a, "b"))
	evs, err := b.client().Events(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var last uint64
	for _, ev := range evs {
		last = ev.Seq
	}
	got := make(chan []engine.Event, 1)
	go func() {
		evs, _ := b.client().Events(last, 10*time.Second) // waits for news
		got <- evs
	}()
	time.Sleep(200 * time.Millisecond)
	a.write("f.md", "x")
	a.client().Add(filepath.Join(a.home, "f.md"), nil)
	select {
	case evs := <-got:
		if len(evs) == 0 || evs[0].Seq <= last {
			t.Fatalf("events %+v", evs)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("long poll did not return on a new event")
	}
}

func TestD20_ProblemsInStatusAndOnTheirOwnRoute(t *testing.T) {
	_, _, b := pair(t)
	b.client().SetMachine(proto.MachineRecord{ID: config.NewID(), Name: "ghost", Target: "ghost"})
	var since time.Time
	eventually(t, "ghost problem in status", func() bool {
		st, err := b.client().Status()
		if err != nil {
			return false
		}
		for _, p := range st.Problems {
			if p.Key == "link:ghost" && p.Severity == problems.Error && strings.Contains(p.Message, "Could not resolve") {
				since = p.Since
				return true
			}
		}
		return false
	})
	if since.IsZero() {
		t.Fatal("since not set")
	}
	ps, err := b.client().Problems()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Key != "link:ghost" || !ps[0].Since.Equal(since) {
		t.Fatalf("GET /problems: %+v (since %v)", ps, since)
	}
}
