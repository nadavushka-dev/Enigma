package engine

import (
	"fmt"
	"strings"
	"time"
)

// What happened while Enigma could not watch an entry (SPEC §13.5): the
// catch-up scan when the daemon starts, when an entry comes back into
// this machine's scope, or when its folder is back after being missing,
// finds changes it did not see live. They are summed up per entry, shown
// as a standing warning for 24 h and sent as one desktop notification.
// Visibility only: nothing about what syncs changes.

// Kinds of catch-up.
const (
	AwayNotRunning = "not running"     // the daemon was stopped
	AwayNotSynced  = "not synced here" // the entry was out of this machine's groups
	AwayMissing    = "missing"         // the entry's folder was missing or a symlink
)

// awayFor is how long a summary stands.
const awayFor = 24 * time.Hour

// AwaySummary is one entry's catch-up findings.
type AwaySummary struct {
	Kind      string    `json:"kind"`
	At        time.Time `json:"at"`
	Deleted   int       `json:"deleted,omitempty"`   // files deleted here, deleted on the other machines too
	Changed   int       `json:"changed,omitempty"`   // files edited here, sent out
	Added     int       `json:"added,omitempty"`     // new files here, sent out
	Replaced  bool      `json:"replaced,omitempty"`  // the folder is another folder now
	Restoring int       `json:"restoring,omitempty"` // files not here, fetched from the other machines
	Strike    int       `json:"strike,omitempty"`    // folder identity changed: strike N of 3 (§6.5)

	restoring map[string]bool // the paths behind Restoring (a scan may look twice)
}

// restore counts rel as being restored.
func (s *AwaySummary) restore(rel string) {
	if s.restoring == nil {
		s.restoring = map[string]bool{}
	}
	s.restoring[rel] = true
	s.Restoring = len(s.restoring)
}

func (s *AwaySummary) any() bool {
	return s.Deleted+s.Changed+s.Added+s.Restoring+s.Strike > 0 || s.Replaced
}

// Text is the summary for people: the problem's message and the
// notification.
func (s *AwaySummary) Text(entry string) string {
	var parts []string
	files := func(n int) string {
		if n == 1 {
			return "1 file"
		}
		return fmt.Sprintf("%d files", n)
	}
	if s.Deleted > 0 {
		parts = append(parts, files(s.Deleted)+" you deleted here were deleted on the other machines")
	}
	if s.Changed > 0 {
		parts = append(parts, files(s.Changed)+" changed here were sent to the other machines")
	}
	if s.Added > 0 {
		parts = append(parts, files(s.Added)+" new here were sent to the other machines")
	}
	if s.Replaced {
		parts = append(parts, "~/"+entry+" was replaced by another folder")
	}
	if s.Restoring > 0 {
		parts = append(parts, files(s.Restoring)+" not here are restored from the other machines")
	}
	if s.Strike > 0 {
		parts = append(parts, fmt.Sprintf("the folder's identity changed (strike %d/%d)", s.Strike, rootFlipLimit))
	}
	what := "while Enigma was not running"
	switch s.Kind {
	case AwayNotSynced:
		what = "while ~/" + entry + " was not synced here"
	case AwayMissing:
		what = "while ~/" + entry + " was missing here"
	}
	return "~/" + entry + ": " + what + ": " + strings.Join(parts, "; ")
}

// beginAway starts collecting what entry's catch-up scan finds. It
// reports whether it started one (false inside another, e.g. at startup);
// the caller that started it ends it.
func (e *Engine) beginAway(entry, kind string) bool {
	if _, ok := e.awayScan[entry]; ok {
		return false
	}
	e.awayScan[entry] = &AwaySummary{Kind: kind, At: e.now()}
	return true
}

// endAway keeps what entry's catch-up scan found, if anything, and tells
// the user once.
func (e *Engine) endAway(entry string) {
	s := e.awayScan[entry]
	delete(e.awayScan, entry)
	if s == nil || !s.any() {
		return
	}
	e.away[entry] = s
	msg := s.Text(entry)
	e.log.Info("changes found that were not seen live", "entry", entry, "summary", msg)
	e.event(EvWhileAway, entry, "", msg)
	e.notify(entry, msg)
}

// awayOf is the summary entry's running catch-up scan adds to; nil when
// none runs (the change was seen live).
func (e *Engine) awayOf(entry string) *AwaySummary { return e.awayScan[entry] }

// awayStatus is entry's summary while it stands.
func (e *Engine) awayStatus(entry string) *AwaySummary {
	s := e.away[entry]
	if s != nil && e.now().Sub(s.At) >= awayFor {
		delete(e.away, entry)
		return nil
	}
	return s
}
