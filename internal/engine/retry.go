package engine

import (
	"sort"
	"strings"
	"time"

	"github.com/nadavushka-dev/enigma/internal/version"
)

// Failed writes (SPEC §6.6): a received version that could not be written
// here (permission denied, disk full) is held and tried again, with a
// backoff, until it is written or another version supersedes it. While it
// fails, the entry's status says so and a standing problem names it.
//
// Only the version is held, never its content: up to 2,000 files × 10 MB
// per entry failing at once (disk full) would otherwise sit in memory. A
// retry asks a peer that is linked and shares the entry for the content
// again (onAnnounce, a fetch), and the write is tried when it arrives.

const (
	writeRetryFirst = 2 * time.Second
	writeRetryMax   = 5 * time.Minute
)

type failedWrite struct {
	p     *peer
	entry string
	in    version.Version
	anc   []version.Node
	err   string
	next  time.Time
	wait  time.Duration
	asked bool // the retry asked a peer for the content (execute, Fetch)
}

// writeFailed holds a write that failed, reports it once (activity event,
// notification) and schedules its retry. The content is not held.
func (e *Engine) writeFailed(p *peer, entry, rel string, in version.Version, anc []version.Node, err error) {
	wait := writeRetryFirst
	old := e.failing[rel]
	if old != nil {
		wait = min(old.wait*2, writeRetryMax)
	}
	e.failing[rel] = &failedWrite{p: p, entry: entry, in: in, anc: anc, err: err.Error(),
		next: time.Now().Add(wait), wait: wait}
	if old == nil || old.err != err.Error() {
		e.log.Error("apply", "path", rel, "err", err)
		e.event(EvWriteFailed, rel, p.name, err.Error())
		e.notify(rel, "could not write ~/"+rel+": "+err.Error())
	} else {
		e.log.Debug("apply still failing", "path", rel, "err", err, "next", wait)
	}
}

// retryFailedWrites tries the held writes that are due, on the poll tick.
// A delete is applied again; a write asks a linked peer sharing the entry
// for the content, and is tried when it arrives (onFile). One that fails
// again is held again with a longer wait; one that works, or is
// superseded, is dropped (setCurrent).
func (e *Engine) retryFailedWrites() {
	now := time.Now()
	for rel, f := range e.failing {
		if now.Before(f.next) {
			continue
		}
		if f.in.IsDeleted() {
			e.onFile(f.p, rel, f.in, f.anc, nil)
			if g := e.failing[rel]; g == f {
				delete(e.failing, rel) // nothing happened: no longer applies
			}
			continue
		}
		// Asked again after the wait if no answer comes (a dropped link).
		f.next = now.Add(f.wait)
		p := e.retryPeer(rel, f.p)
		if p == nil {
			continue // no peer to ask: held, and reported, until one links
		}
		f.p, f.asked = p, false
		e.onAnnounce(p, rel, f.in) // decided again; a fetch if it still applies
		if g := e.failing[rel]; g == f && !f.asked {
			delete(e.failing, rel) // nothing to fetch: no longer applies
		}
	}
}

// retryPeer is the peer a held write of rel asks for its content: the one
// it came from while that is still linked and shares rel, else the first
// (by id) that does; nil when none does.
func (e *Engine) retryPeer(rel string, from *peer) *peer {
	if from != nil && e.peers[from.id] == from && e.sharesPath(rel, from.id) {
		return from
	}
	var ids []string
	for id, q := range e.peers {
		if e.sharesPath(rel, q.id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	return e.peers[ids[0]]
}

// writeFailing lists entry's paths whose write is failing.
func (e *Engine) writeFailing(entry string) []string {
	var out []string
	for rel, f := range e.failing {
		if f.entry == entry || strings.HasPrefix(rel, entry+"/") {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}
