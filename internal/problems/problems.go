// Package problems derives the standing problems of a machine from its
// state (SPEC §13.5, D20): what is wrong right now, with a hint, kept
// until the condition ends.
package problems

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nadavushka-dev/enigma/internal/engine"
)

type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Problem is one standing problem. Key identifies the condition, so the
// same problem keeps its Since across updates.
type Problem struct {
	Key      string    `json:"key"`
	Severity Severity  `json:"severity"`
	Since    time.Time `json:"since"`
	Message  string    `json:"message"`
	Hint     string    `json:"hint,omitempty"`
	Path     string    `json:"path,omitempty"` // relative to ~
	Peer     string    `json:"peer,omitempty"` // machine name
}

// Peer is one member as this machine sees it.
type Peer struct {
	Name      string
	Dials     bool // this machine has a target for it; otherwise it must dial us
	Connected bool
	LastError string
}

// Input is the state problems are derived from.
type Input struct {
	Peers   []Peer
	Entries []engine.EntryStatus
	Limits  engine.Limits
	Events  []engine.Event // recent activity; conflicts and errors count for an hour
}

// recent is how long a conflict copy or an error event stays a problem.
const recent = time.Hour

// Derive lists the problems in in, errors first, then by key. Since is
// left zero; Tracker fills it.
func Derive(in Input, now time.Time) []Problem {
	var ps []Problem
	for _, p := range in.Peers {
		if p.Connected {
			continue
		}
		switch {
		case strings.Contains(p.LastError, "cannot upgrade"):
			ps = append(ps, Problem{Key: "upgrade:" + p.Name, Severity: Error, Peer: p.Name,
				Message: p.LastError,
				Hint:    "run make dist, or enigma update on a machine with internet access, so a binary for its platform is at hand"})
		case !p.Dials:
			ps = append(ps, Problem{Key: "link:" + p.Name, Severity: Warning, Peer: p.Name,
				Message: p.Name + " link down: it has no ssh target, so only it can dial",
				Hint:    fmt.Sprintf("enigma machine alias %s <ssh-alias> gives this machine a way to reach it", p.Name)})
		default:
			msg := p.Name + " link down"
			if p.LastError != "" {
				msg += " (" + p.LastError + ")"
			}
			ps = append(ps, Problem{Key: "link:" + p.Name, Severity: Error, Peer: p.Name, Message: msg,
				Hint: "enigma doctor checks the links between every machine; changes still flow through the machines both are linked to"})
		}
	}
	for _, e := range in.Entries {
		switch {
		case e.Missing:
			ps = append(ps, Problem{Key: "missing:" + e.Path, Severity: Error, Path: e.Path,
				Message: e.Path + ": missing locally, deletes are not propagated",
				Hint:    "put it back (an unmounted disk, a moved folder?), or enigma remove it"})
		case len(e.OverLimit) > 0:
			ps = append(ps, Problem{Key: "overlimit:" + e.Path, Severity: Error, Path: e.Path,
				Message: fmt.Sprintf("%s: over the %s-file limit, %s not synced", e.Path, engine.FormatCount(in.Limits.MaxFiles), plural(len(e.OverLimit), "new file")),
				Hint:    "remove files, or raise the limit for the whole fleet with enigma limits --files N"})
		case len(e.Skipped) > 0:
			ps = append(ps, Problem{Key: "skipped:" + e.Path, Severity: Warning, Path: e.Path,
				Message: fmt.Sprintf("%s: %s skipped (symlinks, or over %s)", e.Path, plural(len(e.Skipped), "file"), engine.FormatSize(in.Limits.MaxFileSize)),
				Hint:    "enigma status --json lists them; enigma limits --file-size raises the size limit"})
		}
	}
	for _, ev := range in.Events {
		if now.Sub(ev.Time) > recent {
			continue
		}
		switch ev.Kind {
		case engine.EvConflict:
			ps = append(ps, Problem{Key: "conflict:" + ev.Path, Severity: Warning, Path: ev.Path, Peer: ev.Peer,
				Message: fmt.Sprintf("conflict on %s: the other edit is kept as %s", ev.Detail, ev.Path),
				Hint:    "enigma conflicts lists every copy; delete the copy once you have what you need from it"})
		case engine.EvError:
			ps = append(ps, Problem{Key: fmt.Sprintf("error:%d", ev.Seq), Severity: Error, Path: ev.Path, Peer: ev.Peer,
				Message: errorMessage(ev)})
		}
	}
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Severity != ps[j].Severity {
			return ps[i].Severity == Error
		}
		return ps[i].Key < ps[j].Key
	})
	return ps
}

func errorMessage(ev engine.Event) string {
	switch {
	case ev.Path != "" && ev.Detail != "":
		return ev.Path + ": " + ev.Detail
	case ev.Path != "":
		return ev.Path
	default:
		return ev.Detail
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// Tracker remembers when each problem was first seen, so Since survives
// across updates while the problem stands.
type Tracker struct {
	since map[string]time.Time
}

// Update derives the problems of in and stamps each with the time it was
// first seen. A problem that is gone is forgotten; if it comes back, it
// is new.
func (t *Tracker) Update(in Input, now time.Time) []Problem {
	ps := Derive(in, now)
	next := make(map[string]time.Time, len(ps))
	for i := range ps {
		s, ok := t.since[ps[i].Key]
		if !ok {
			s = now
		}
		next[ps[i].Key] = s
		ps[i].Since = s
	}
	t.since = next
	return ps
}

// Summary is the one-line form for a notice line: "all good", or the
// count and the first (most severe, then oldest key) problem.
func Summary(ps []Problem) string {
	if len(ps) == 0 {
		return "all good"
	}
	return fmt.Sprintf("%s: %s", plural(len(ps), "problem"), ps[0].Message)
}
