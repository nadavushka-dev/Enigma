package engine

import (
	"fmt"
	"strings"
	"time"
)

// What happened while Enigma could not watch an entry (SPEC §13.5): the
// catch-up scan when the daemon starts, when an entry comes back into
// this machine's scope (a group change, the entry moved), or when its
// folder is back after being missing, finds changes it did not see live.
// They are summed up per entry, shown as a standing warning for 24 h
// (kept in the state, so a restart does not clear it) and sent as one
// desktop notification. Visibility only: nothing about what syncs changes.

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
	Kind     string    `json:"kind"`
	At       time.Time `json:"at"`
	Deleted  int       `json:"deleted,omitempty"`  // files deleted here, deleted on the other machines too
	Changed  int       `json:"changed,omitempty"`  // files edited here, sent out
	Added    int       `json:"added,omitempty"`    // new files here, sent out
	Replaced bool      `json:"replaced,omitempty"` // the folder is another folder now
	// Restored: files not here that were written back from the other
	// machines; Restoring: those still asked for (one the other machines
	// deleted meanwhile is neither).
	Restored  int `json:"restored,omitempty"`
	Restoring int `json:"restoring,omitempty"`
	Strike    int `json:"strike,omitempty"` // folder identity changed: strike N of 3 (§6.5)

	// Waiting: the paths behind Restoring (saved with the state; a scan
	// may look twice).
	Waiting map[string]bool `json:"waiting,omitempty"`
	// before: the entry's paths already waiting for a restore when the
	// scan began; not news (a restart in the middle of a restore).
	before map[string]bool
}

// restore counts rel as being restored, unless it already was before the
// catch-up.
func (s *AwaySummary) restore(rel string) {
	if s.before[rel] {
		return
	}
	if s.Waiting == nil {
		s.Waiting = map[string]bool{}
	}
	s.Waiting[rel] = true
	s.Restoring = len(s.Waiting)
}

func (s *AwaySummary) any() bool {
	return s.Deleted+s.Changed+s.Added+s.Restored+s.Restoring+s.Strike > 0 || s.Replaced
}

// Text is the summary for people, as a notification: the entry, then
// Detail.
func (s *AwaySummary) Text(entry string) string {
	return "~/" + entry + ": " + s.Detail(entry)
}

// Detail is the summary without the entry in front: the problem's
// message puts the entry's path there itself.
func (s *AwaySummary) Detail(entry string) string {
	var parts []string
	// files says n files and agrees the verb with them.
	files := func(n int, adj, rest, one, many string) string {
		verb, noun := many, "files"
		if n == 1 {
			verb, noun = one, "file"
		}
		if adj != "" {
			noun = adj + " " + noun
		}
		return fmt.Sprintf("%d %s %s %s", n, noun, rest, verb)
	}
	if s.Deleted > 0 {
		parts = append(parts, files(s.Deleted, "", "you deleted here", "was", "were")+" deleted on the other machines")
	}
	if s.Changed > 0 {
		parts = append(parts, files(s.Changed, "", "changed here", "was", "were")+" sent to the other machines")
	}
	if s.Added > 0 {
		parts = append(parts, files(s.Added, "new", "here", "was", "were")+" sent to the other machines")
	}
	if s.Replaced {
		parts = append(parts, "~/"+entry+" was replaced by another folder")
	}
	if s.Restored > 0 {
		parts = append(parts, files(s.Restored, "", "not here", "was", "were")+" restored from the other machines")
	}
	switch {
	case s.Restoring > 0 && s.Restored > 0:
		parts = append(parts, "more are being restored")
	case s.Restoring > 0:
		// No count: one the other machines deleted meanwhile is not
		// restored, and only the ones written back are counted.
		parts = append(parts, "files not here are being restored from the other machines")
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
	return what + ": " + strings.Join(parts, "; ")
}

// beginAway starts collecting what entry's catch-up scan finds. It
// reports whether it started one (false inside another, e.g. at startup);
// the caller that started it ends it.
func (e *Engine) beginAway(entry, kind string) bool {
	if _, ok := e.awayScan[entry]; ok {
		return false
	}
	s := &AwaySummary{Kind: kind, At: e.now()}
	if kind != AwayMissing {
		// Back from missing, its files were set aside while it was gone:
		// news. Otherwise, a file already waiting was so before.
		s.before = map[string]bool{}
		for rel, fs := range e.st.Files {
			if !fs.Materialized && !fs.V.IsDeleted() && (rel == entry || strings.HasPrefix(rel, entry+"/")) {
				s.before[rel] = true
			}
		}
	}
	e.awayScan[entry] = s
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
	s.before = nil
	e.st.Away[entry] = s
	e.dirty = true
	msg := s.Text(entry)
	e.log.Info("changes found that were not seen live", "entry", entry, "summary", msg)
	e.event(EvWhileAway, entry, "", msg)
	e.notify(entry, msg)
}

// awayOf is the summary entry's running catch-up scan adds to; nil when
// none runs (the change was seen live).
func (e *Engine) awayOf(entry string) *AwaySummary { return e.awayScan[entry] }

// awayRestored: rel, which a summary counts as being restored, was
// written back here.
func (e *Engine) awayRestored(entry, rel string) {
	for _, s := range []*AwaySummary{e.awayScan[entry], e.st.Away[entry]} {
		if s != nil && s.Waiting[rel] {
			delete(s.Waiting, rel)
			s.Restoring = len(s.Waiting)
			s.Restored++
			e.dirty = true
		}
	}
}

// awayNotWaiting: rel is not waiting for a restore any more (another
// version, a delete, here on disk), without counting it as restored.
func (e *Engine) awayNotWaiting(rel string) {
	if len(e.st.Away) == 0 && len(e.awayScan) == 0 {
		return
	}
	entry, _, ok := e.entryFor(rel)
	if !ok {
		return
	}
	for _, s := range []*AwaySummary{e.awayScan[entry], e.st.Away[entry]} {
		if s != nil && s.Waiting[rel] {
			delete(s.Waiting, rel)
			s.Restoring = len(s.Waiting)
			e.dirty = true
		}
	}
}

// awayStatus is a copy of entry's summary while it stands.
func (e *Engine) awayStatus(entry string) *AwaySummary {
	s := e.st.Away[entry]
	if s == nil {
		return nil
	}
	if e.now().Sub(s.At) >= awayFor {
		delete(e.st.Away, entry)
		e.dirty = true
		return nil
	}
	if !s.any() {
		return nil // only restores, and none of them were
	}
	c := *s
	c.Waiting, c.before = nil, nil
	return &c
}
