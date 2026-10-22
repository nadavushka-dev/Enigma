package engine

import "time"

// maxEvents is how many recent events a machine keeps (SPEC §12.2).
const maxEvents = 500

// Event kinds, for the TUI's activity pane.
const (
	EvReceived     = "received"      // a peer's change was written here
	EvChanged      = "changed"       // a change made here was sent out
	EvDeleted      = "deleted"       // deleted here (Peer: by whom; empty: by the user)
	EvMerged       = "merged"        // concurrent edits merged cleanly
	EvConflict     = "conflict"      // a conflict copy was kept (Path: the copy, Detail: the original)
	EvEntryAdded   = "entry-added"   // a file or folder started syncing
	EvEntryRemoved = "entry-removed" // ... stopped syncing
	EvLinkUp       = "link-up"
	EvLinkDown     = "link-down"
	EvUpgrade      = "upgrade"
	EvLimits       = "limits" // the group's limits changed (Detail: the new ones)
	EvError        = "error"
)

// Event is one thing that happened on this machine.
type Event struct {
	Seq    uint64    `json:"seq"`
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"`
	Path   string    `json:"path,omitempty"` // relative to ~
	Peer   string    `json:"peer,omitempty"` // machine name
	Detail string    `json:"detail,omitempty"`
}

// event records an event; loop only.
func (e *Engine) event(kind, path, peer, detail string) {
	e.evSeq++
	e.events = append(e.events, Event{Seq: e.evSeq, Time: time.Now(), Kind: kind, Path: path, Peer: peer, Detail: detail})
	if len(e.events) > maxEvents {
		e.events = append(e.events[:0:0], e.events[len(e.events)-maxEvents:]...)
	}
}

// Events returns the kept events newer than seq after, oldest first.
func (e *Engine) Events(after uint64) []Event {
	var out []Event
	e.do(func() {
		for _, ev := range e.events {
			if ev.Seq > after {
				out = append(out, ev)
			}
		}
	})
	return out
}

// notify raises a desktop notification about path (SPEC §12.3); loop only.
func (e *Engine) notify(path, body string) {
	if e.o.Notify != nil {
		e.o.Notify(path, "Enigma", body)
	}
}
