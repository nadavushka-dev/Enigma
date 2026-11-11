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

const (
	writeRetryFirst = 2 * time.Second
	writeRetryMax   = 5 * time.Minute
)

type failedWrite struct {
	p       *peer
	entry   string
	in      version.Version
	anc     []version.Node
	content []byte
	err     string
	next    time.Time
	wait    time.Duration
}

// writeFailed holds a write that failed, reports it once (activity event,
// notification) and schedules its retry.
func (e *Engine) writeFailed(p *peer, entry, rel string, in version.Version, anc []version.Node, content []byte, err error) {
	wait := writeRetryFirst
	old := e.failing[rel]
	if old != nil {
		wait = min(old.wait*2, writeRetryMax)
	}
	e.failing[rel] = &failedWrite{p: p, entry: entry, in: in, anc: anc, content: content, err: err.Error(),
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
// One that fails again is held again with a longer wait; one that works,
// or is superseded, is dropped (setCurrent).
func (e *Engine) retryFailedWrites() {
	now := time.Now()
	for rel, f := range e.failing {
		if now.Before(f.next) {
			continue
		}
		if f.content == nil && !f.in.IsDeleted() {
			e.onAnnounce(f.p, rel, f.in) // a delete or a fetch: decided again
		} else {
			e.onFile(f.p, rel, f.in, f.anc, f.content)
		}
		if g := e.failing[rel]; g == f {
			// Nothing happened (no longer applies): stop trying.
			delete(e.failing, rel)
		}
	}
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
