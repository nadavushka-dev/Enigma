package engine

import (
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/proto"
	"github.com/nadavushka-dev/enigma/internal/version"
)

// blackhole is a link end that can go silent like a dead network: after
// cut, nothing more is delivered in either direction, and nothing errors.
type blackhole struct {
	net.Conn
	mu  sync.Mutex
	cut bool
}

func (b *blackhole) dead() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.cut }

func (b *blackhole) Read(p []byte) (int, error) {
	n, err := b.Conn.Read(p)
	if b.dead() {
		select {} // a dead network never answers
	}
	return n, err
}

func (b *blackhole) Write(p []byte) (int, error) {
	if b.dead() {
		return len(p), nil // swallowed
	}
	return b.Conn.Write(p)
}

func TestDeadLinkIsDroppedByHeartbeat(t *testing.T) {
	fast := func(o *Options) { o.Heartbeat = 50 * time.Millisecond }
	a, b := newMachine(t, "a", fast), newMachine(t, "b", fast)
	c1, c2 := net.Pipe()
	ha, hb := &blackhole{Conn: c1}, &blackhole{Conn: c2}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.e.Connect(ha, true) }()
	go func() { defer wg.Done(); b.e.Connect(hb, false) }()
	wg.Wait()
	eventually(t, "linked", func() bool { return len(a.e.Status().Peers) == 1 && len(b.e.Status().Peers) == 1 })
	time.Sleep(200 * time.Millisecond) // pings keep a live link up
	if len(a.e.Status().Peers) != 1 || len(b.e.Status().Peers) != 1 {
		t.Fatal("a live, idle link was dropped")
	}
	for _, h := range []*blackhole{ha, hb} {
		h.mu.Lock()
		h.cut = true
		h.mu.Unlock()
	}
	eventually(t, "both sides drop the dead link", func() bool {
		return len(a.e.Status().Peers) == 0 && len(b.e.Status().Peers) == 0
	})
}

var _ io.ReadWriteCloser = (*blackhole)(nil)

// Regression: pings were sent from the engine loop, so a machine busy on a
// long operation (hashing a big folder) stopped pinging and every peer
// dropped the link as silent once a minute. Liveness is the connection's,
// not the loop's: a busy loop, even with its inbox full, keeps its links.
func TestBusyLoopKeepsItsLinks(t *testing.T) {
	const hb = 30 * time.Millisecond
	fast := func(o *Options) { o.Heartbeat = hb }
	a, b := newMachine(t, "a", fast), newMachine(t, "b", fast)
	link(t, a, b)
	eventually(t, "linked", func() bool { return len(a.e.Status().Peers) == 1 && len(b.e.Status().Peers) == 1 })

	busy := make(chan struct{})
	go a.e.do(func() { <-busy })
	// fill a's inbox from b while a is busy: more frames than the inbox holds
	b.e.do(func() {
		for _, p := range b.e.peers {
			for i := range 600 {
				v := version.Version{Hash: fmt.Sprint(i)}
				p.send(&proto.Msg{Type: proto.Announce, Path: "nowhere/" + fmt.Sprint(i), Version: &v})
			}
		}
	})
	time.Sleep(12 * hb) // four silence windows
	close(busy)

	for _, m := range []*machine{a, b} {
		for _, ev := range m.e.Events(0) {
			if ev.Kind == EvLinkDown {
				t.Fatalf("%s dropped the link while a was busy", m.name)
			}
		}
	}
	eventually(t, "still linked", func() bool { return len(a.e.Status().Peers) == 1 && len(b.e.Status().Peers) == 1 })
}

// writeFails is a link end whose writes fail after cut while reads hang,
// as when the peer closed its end but our read is stuck in the network.
type writeFails struct {
	net.Conn
	mu  sync.Mutex
	cut bool
}

func (w *writeFails) dead() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.cut }

func (w *writeFails) Read(p []byte) (int, error) {
	n, err := w.Conn.Read(p)
	if w.dead() {
		select {}
	}
	return n, err
}

func (w *writeFails) Write(p []byte) (int, error) {
	if w.dead() {
		return 0, io.ErrClosedPipe
	}
	return w.Conn.Write(p)
}

// Regression: a write error closed the peer without detaching it, so when
// the read was stuck too the link stayed "up" forever (the heartbeat had
// exited with the peer). Whatever closes a peer must drop the link.
func TestWriteFailureDropsTheLinkEvenWhenReadsHang(t *testing.T) {
	fast := func(o *Options) { o.Heartbeat = 50 * time.Millisecond }
	a, b := newMachine(t, "a", fast), newMachine(t, "b", fast)
	c1, c2 := net.Pipe()
	wa := &writeFails{Conn: c1}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.e.Connect(wa, true) }()
	go func() { defer wg.Done(); b.e.Connect(c2, false) }()
	wg.Wait()
	eventually(t, "linked", func() bool { return len(a.e.Status().Peers) == 1 })
	wa.mu.Lock()
	wa.cut = true
	wa.mu.Unlock()
	eventually(t, "a drops the link after its write failed", func() bool { return len(a.e.Status().Peers) == 0 })
}
