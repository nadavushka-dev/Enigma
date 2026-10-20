package engine

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nadavushka-dev/enigma/internal/proto"
	"github.com/nadavushka-dev/enigma/internal/version"
)

const handshakeTimeout = 10 * time.Second

// summaryChunk bounds the size of one catch-up Summary frame.
const summaryChunk = 500

// peer is one live link. Sending never blocks the engine loop: messages go
// into an unbounded queue drained by a writer goroutine.
type peer struct {
	id, name string
	dialedBy string // machine ID of the side that dialed this link
	conn     io.ReadWriteCloser
	sent     *atomic.Int64

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []*proto.Msg
	closed bool
}

func newPeer(id, name, dialedBy string, conn io.ReadWriteCloser, sent *atomic.Int64) *peer {
	p := &peer{id: id, name: name, dialedBy: dialedBy, conn: conn, sent: sent}
	p.cond = sync.NewCond(&p.mu)
	go p.writeLoop()
	return p
}

func (p *peer) send(m *proto.Msg) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.queue = append(p.queue, m)
	p.cond.Signal()
}

func (p *peer) close() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		p.cond.Signal()
	}
	p.mu.Unlock()
	p.conn.Close()
}

func (p *peer) writeLoop() {
	for {
		p.mu.Lock()
		for len(p.queue) == 0 && !p.closed {
			p.cond.Wait()
		}
		if p.closed {
			p.mu.Unlock()
			return
		}
		batch := p.queue
		p.queue = nil
		p.mu.Unlock()
		for _, m := range batch {
			if err := proto.Write(p.conn, m); err != nil {
				p.close()
				return
			}
			p.sent.Add(1)
		}
	}
}

// ErrDuplicateLink: the peer is already linked through the link both sides
// prefer (SPEC §5); this one was closed.
var ErrDuplicateLink = errors.New("already linked to this peer")

// Connect runs the handshake on conn and, on success, attaches the link to
// the engine. dialed says whether this side opened conn. It returns once
// the link is up; the link then lives until conn fails or the engine closes.
func (e *Engine) Connect(conn io.ReadWriteCloser, dialed bool) error {
	hello := &proto.Msg{Type: proto.Hello, Hello: &proto.HelloBody{
		GroupID: e.o.GroupID, MachineID: e.o.MachineID, Name: e.o.Name, Proto: proto.Version}}
	sendErr := make(chan error, 1)
	go func() { sendErr <- proto.Write(conn, hello) }()

	type res struct {
		m   *proto.Msg
		err error
	}
	got := make(chan res, 1)
	go func() { m, err := proto.Read(conn); got <- res{m, err} }()

	var r res
	select {
	case r = <-got:
	case <-time.After(handshakeTimeout):
		conn.Close()
		return errors.New("handshake timed out")
	}
	if err := <-sendErr; err != nil {
		conn.Close()
		return fmt.Errorf("handshake: %w", err)
	}
	if r.err != nil {
		conn.Close()
		return fmt.Errorf("handshake: %w", r.err)
	}
	h := r.m.Hello
	switch {
	case r.m.Type != proto.Hello || h == nil:
		r.err = errors.New("handshake: expected hello")
	case h.Proto != proto.Version:
		r.err = fmt.Errorf("protocol mismatch: peer v%d, local v%d (upgrade enigma on both machines)", h.Proto, proto.Version)
	case h.GroupID != e.o.GroupID:
		r.err = fmt.Errorf("peer %q belongs to another group", h.Name)
	case h.MachineID == e.o.MachineID:
		r.err = errors.New("connected to itself")
	}
	if r.err != nil {
		conn.Close()
		return r.err
	}

	dialedBy := h.MachineID
	if dialed {
		dialedBy = e.o.MachineID
	}
	p := newPeer(h.MachineID, h.Name, dialedBy, conn, &e.sent)
	kept := false
	if err := e.do(func() { kept = e.attach(p) }); err != nil {
		p.close()
		return err
	}
	if !kept {
		p.close()
		return ErrDuplicateLink
	}
	go e.readLoop(p)
	return nil
}

func (e *Engine) readLoop(p *peer) {
	for {
		m, err := proto.Read(p.conn)
		if err != nil {
			p.close()
			e.post(func() { e.detach(p) })
			return
		}
		e.received.Add(1)
		e.post(func() { e.handleMsg(p, m) })
	}
}

// attach registers p and starts the catch-up (§6.5): group state first,
// then a summary of every tracked file. With two links to one peer, both
// sides keep the one dialed by the lower machine ID; between two links
// dialed by the same side, the newer wins (the old one is likely dead).
// It reports whether p was kept.
func (e *Engine) attach(p *peer) bool {
	if old := e.peers[p.id]; old != nil {
		preferred := min(e.o.MachineID, p.id)
		if old.dialedBy == preferred && p.dialedBy != preferred {
			return false
		}
		old.close()
	}
	e.peers[p.id] = p
	e.log.Info("peer up", "peer", p.name)

	recs := make([]proto.EntryRecord, 0, len(e.st.Entries))
	for _, r := range e.st.Entries {
		recs = append(recs, r)
	}
	p.send(&proto.Msg{Type: proto.Entries, Entries: recs})
	chunk := map[string]version.Version{}
	flush := func() {
		if len(chunk) > 0 {
			p.send(&proto.Msg{Type: proto.Summary, Summary: chunk})
			chunk = map[string]version.Version{}
		}
	}
	for path, fs := range e.st.Files {
		chunk[path] = fs.V
		if len(chunk) >= summaryChunk {
			flush()
		}
	}
	flush()
	p.send(&proto.Msg{Type: proto.SummaryDone})
	return true
}

func (e *Engine) detach(p *peer) {
	if e.peers[p.id] == p {
		delete(e.peers, p.id)
		e.log.Info("peer down", "peer", p.name)
	}
}

// broadcast sends m to every peer except skip (may be nil).
func (e *Engine) broadcast(m *proto.Msg, skip *peer) {
	for _, p := range e.peers {
		if p != skip {
			p.send(m)
		}
	}
}

func (e *Engine) handleMsg(p *peer, m *proto.Msg) {
	if e.peers[p.id] != p {
		return // superseded link
	}
	switch m.Type {
	case proto.Entries:
		for _, r := range m.Entries {
			e.applyEntry(r, p)
		}
	case proto.Summary:
		for path, v := range m.Summary {
			e.onAnnounce(p, path, v)
		}
	case proto.SummaryDone:
	case proto.Announce:
		if m.Version != nil {
			e.onAnnounce(p, m.Path, *m.Version)
		}
	case proto.Fetch:
		e.onFetch(p, m.Path)
	case proto.File:
		if m.Version != nil {
			e.onFile(p, m.Path, *m.Version, m.Content)
		}
	default:
		e.log.Warn("unknown message", "type", m.Type, "peer", p.name)
	}
}
