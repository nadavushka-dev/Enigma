package engine

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"
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
