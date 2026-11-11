// Package problems derives the standing problems of a machine from its
// state (SPEC §13.5, D20): what is wrong right now, with a hint, kept
// until the condition ends.
package problems

import (
	"fmt"
	"slices"
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
	// Conflicts are the conflict copies on disk now; with ConflictsKnown, a
	// conflict is a problem only while its copy is there.
	Conflicts      []string
	ConflictsKnown bool
}

// recent is how long a conflict copy or an error event stays a problem.
const recent = time.Hour

// unwatchedHint goes with a folder, or part of one, not watched.
const unwatchedHint = "Enigma keeps trying to watch it; on Linux the inotify watch limit may be reached " +
	"(sysctl fs.inotify.max_user_watches); if this stays, check enigma.log"

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
		ps = append(ps, linkProblems(e, now)...)
		switch {
		case e.Missing:
		case e.Unwatched:
			ps = append(ps, Problem{Key: "unwatched:" + e.Path, Severity: Error, Path: e.Path,
				Message: e.Path + ": changes here are not watched; they sync only at the next check",
				Hint:    unwatchedHint})
		case len(e.UnwatchedFolders) > 0:
			ps = append(ps, Problem{Key: "unwatched:" + e.Path, Severity: Error, Path: e.Path,
				Message: fmt.Sprintf("%s: %s not watched (%s); changes there sync only at the next check",
					e.Path, plural(len(e.UnwatchedFolders), "subfolder"), strings.Join(e.UnwatchedFolders, ", ")),
				Hint: unwatchedHint})
		}
		if e.IdentityUnreliable {
			ps = append(ps, Problem{Key: "identity:" + e.Path, Severity: Warning, Path: e.Path,
				Message: e.Path + ": the folder's identity keeps changing here, so a replaced folder is not told apart",
				Hint:    "usual on network or FUSE file systems; deleting or replacing the whole folder here then reads as deleting its files"})
		}
		if len(e.Restoring) > 0 && !e.Missing {
			ps = append(ps, Problem{Key: "restoring:" + e.Path, Severity: Warning, Path: e.Path,
				Message: fmt.Sprintf("%s: %s not here yet, being restored from the other machines", e.Path, plural(len(e.Restoring), "file")),
				Hint:    "they were behind a symlink, or the folder was replaced; they come back once a machine that has them is linked (enigma status shows the links)"})
		}
		if len(e.Links) > 0 { // a link with a state is reported on its own
			listed := map[string]bool{}
			for _, l := range e.Links {
				listed[l.Path] = true
			}
			var rest []string
			for _, s := range e.Skipped {
				if !listed[s] {
					rest = append(rest, s)
				}
			}
			e.Skipped = rest
		}
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
			if in.ConflictsKnown && !slices.Contains(in.Conflicts, ev.Path) {
				continue // the copy is gone: resolved
			}
			ps = append(ps, Problem{Key: "conflict:" + ev.Path, Severity: Warning, Path: ev.Path, Peer: ev.Peer,
				Message: fmt.Sprintf("conflict on %s: the other edit is kept as %s", ev.Detail, ev.Path),
				Hint:    "enigma conflicts lists every copy; delete the copy once you have what you need from it"})
		case engine.EvError:
			ps = append(ps, Problem{Key: fmt.Sprintf("error:%d", ev.Seq), Severity: Error, Path: ev.Path, Peer: ev.Peer,
				Message: errorMessage(ev)})
		}
	}
	ps = dedupe(ps)
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Severity != ps[j].Severity {
			return ps[i].Severity == Error
		}
		return ps[i].Key < ps[j].Key
	})
	return ps
}

// linkProblems are the standing problems of an entry's links (D23):
// skipped and dangling links and links waiting over a minute are
// warnings; a link blocked by a folder is an error.
func linkProblems(e engine.EntryStatus, now time.Time) []Problem {
	var ps []Problem
	for _, l := range e.Links {
		switch l.State {
		case engine.LinkSkipped:
			ps = append(ps, Problem{Key: "link-skipped:" + l.Path, Severity: Warning, Path: l.Path,
				Message: fmt.Sprintf("%s: link not synced: %s", l.Path, l.Reason),
				Hint:    "a link syncs when its target is inside a synced entry of the same group, under ~; or remove the link"})
		case engine.LinkPending:
			if now.Sub(l.Since) < engine.PendingWarnAfter {
				continue
			}
			ps = append(ps, Problem{Key: "link-pending:" + l.Path, Severity: Warning, Path: l.Path,
				Message: fmt.Sprintf("%s: link waiting for its target (-> %s)", l.Path, l.Target),
				Hint:    "it is created once its target syncs here; enigma status shows the links and machines"})
		case engine.LinkBlocked:
			ps = append(ps, Problem{Key: "link-blocked:" + l.Path, Severity: Error, Path: l.Path,
				Message: fmt.Sprintf("%s: a link elsewhere (-> %s), %s", l.Path, l.Target, l.Reason),
				Hint:    "keep one: delete the folder here, or the link on the other machine; Enigma never replaces a folder"})
		case engine.LinkDangling:
			ps = append(ps, Problem{Key: "link-dangling:" + l.Path, Severity: Warning, Path: l.Path,
				Message: fmt.Sprintf("%s: dangling link (-> %s): %s", l.Path, l.Target, l.Reason),
				Hint:    "Enigma leaves it alone; restore the target or delete the link"})
		}
	}
	return ps
}

// dedupe keeps the first problem of each key: one condition, one line.
func dedupe(ps []Problem) []Problem {
	seen := map[string]bool{}
	out := ps[:0]
	for _, p := range ps {
		if !seen[p.Key] {
			seen[p.Key] = true
			out = append(out, p)
		}
	}
	return out
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
