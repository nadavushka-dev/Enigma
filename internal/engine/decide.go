// Package engine makes and applies sync decisions on one machine (SPEC §6, §7).
package engine

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nadavushka-dev/enigma/internal/version"
)

type Action int

const (
	Ignore Action = iota // incoming is old news
	Record               // set Result as current; disk already matches it
	Fetch                // need the incoming content before deciding
	Apply                // write the incoming content, then set Result
	Delete               // move the local file to the trash, then set Result
)

func (a Action) String() string {
	return [...]string{"ignore", "record", "fetch", "apply", "delete"}[a]
}

// Copy is a conflict copy to create (SPEC §7 step 4).
type Copy struct {
	Path         string          // relative to ~
	FromIncoming bool            // content comes from the incoming version, else from local disk
	Version      version.Version // the copy's version, identical on every machine
}

type Decision struct {
	Action  Action
	Result  version.Version // the new current version (Record/Apply/Delete)
	Backup  bool            // back up the local content first (D7, D13)
	Copy    *Copy
	Content []byte // Apply writes this (a merge result) instead of the incoming content
}

// Merged is a clean three-way merge of the local and incoming content.
type Merged struct {
	Hash    string
	Content []byte
}

// Decide is the whole sync decision table. cur is this machine's current
// version of path (nil if untracked), in is a version from a peer, and
// haveContent says whether in's content is at hand. It is pure and
// deterministic: two machines deciding about the same pair of versions reach
// the same Result and Copy, so a conflict is resolved once (SPEC §7).
func Decide(p string, cur *version.Version, in version.Version, haveContent bool) Decision {
	return decide(p, cur, in, haveContent, nil)
}

// DecideMerged is Decide with the incoming content at hand and a clean merge
// of it with the local content (SPEC §7 step 3). The merge is used only where
// the fallback would otherwise make a conflict copy.
func DecideMerged(p string, cur *version.Version, in version.Version, m Merged) Decision {
	return decide(p, cur, in, true, &m)
}

func decide(p string, cur *version.Version, in version.Version, haveContent bool, merged *Merged) Decision {
	need := func(d Decision) Decision {
		if !haveContent {
			return Decision{Action: Fetch}
		}
		return d
	}
	if cur == nil {
		if in.IsDeleted() {
			return Decision{Action: Record, Result: in}
		}
		return need(Decision{Action: Apply, Result: in})
	}

	ord := version.Compare(in.Vector, cur.Vector)
	if ord == version.Equal && !sameContent(in, *cur) {
		ord = version.Concurrent // should not happen; resolve rather than diverge
	}
	switch ord {
	case version.Before, version.Equal:
		return Decision{Action: Ignore}
	case version.After:
		switch {
		case sameContent(in, *cur):
			return Decision{Action: Record, Result: in}
		case in.IsDeleted():
			return Decision{Action: Delete, Result: in}
		}
		// A preexisting copy was never agreed on by anyone, so whoever
		// superseded it may not have seen its content: keep it (D7, D13).
		return need(Decision{Action: Apply, Result: in, Backup: cur.Origin == version.Preexisting})
	}

	// Concurrent.
	resolved := func(w version.Version) version.Version {
		w.Vector = version.Merge(cur.Vector, in.Vector)
		w.Parents = sortedPair(cur.Hash, in.Hash)
		return w
	}
	switch {
	case sameContent(in, *cur):
		w := *cur
		if in.By > cur.By {
			w = in
		}
		if w.Origin == version.Preexisting { // content is agreed now
			w.Origin = version.Edit
		}
		return Decision{Action: Record, Result: resolved(w)}
	case in.IsDeleted(): // D8: the edit beats the delete
		return Decision{Action: Record, Result: resolved(*cur)}
	case cur.IsDeleted():
		return need(Decision{Action: Apply, Result: resolved(in)})
	case in.Origin == version.Seed && cur.Origin == version.Preexisting: // D7
		return need(Decision{Action: Apply, Result: resolved(in), Backup: true})
	case cur.Origin == version.Seed && in.Origin == version.Preexisting:
		return Decision{Action: Record, Result: resolved(*cur)}
	}

	if !haveContent {
		return Decision{Action: Fetch}
	}
	incomingWins := wins(in, *cur)
	winner, loser := *cur, in
	if incomingWins {
		winner, loser = in, *cur
	}
	if merged != nil {
		// Only data both machines have, so both make this exact version.
		w := resolved(winner)
		w.Hash, w.Mtime, w.Origin = merged.Hash, max(cur.Mtime, in.Mtime), version.Edit
		if sameContent(w, *cur) {
			return Decision{Action: Record, Result: w}
		}
		return Decision{Action: Apply, Result: w, Content: merged.Content}
	}

	// D4 fallback: the newer edit wins, the other is kept as a copy.
	cv := loser
	cv.Vector = version.Merge(cur.Vector, in.Vector)
	cv.Parents, cv.Origin = nil, version.Edit
	d := Decision{
		Result: resolved(winner),
		Copy:   &Copy{Path: ConflictName(p, loser.ByName, loser.Mtime, loser.Hash), FromIncoming: !incomingWins, Version: cv},
	}
	if incomingWins {
		d.Action = Apply
	} else {
		d.Action = Record
	}
	return d
}

// wins is the step-4 order: later mtime, then higher machine ID, then higher
// hash. It must be total: two versions with equal vectors, mtimes and
// authors (one machine merged, the other could not) must still rank.
func wins(a, b version.Version) bool {
	if a.Mtime != b.Mtime {
		return a.Mtime > b.Mtime
	}
	if a.By != b.By {
		return a.By > b.By
	}
	return a.Hash > b.Hash
}

func sameContent(a, b version.Version) bool {
	return a.Hash == b.Hash && (a.IsDeleted() || a.Exec == b.Exec)
}

func sortedPair(a, b string) []string {
	s := []string{a, b}
	sort.Strings(s)
	return s
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ConflictName builds `<stem>.conflict-<machine>-<YYYYMMDD-HHMMSS>-<hash6><ext>`
// next to p. Time in UTC and the loser's content hash make it identical on
// every machine and unique per content, even within one second.
func ConflictName(p, machine string, mtime int64, hash string) string {
	dir, base := path.Split(p)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" { // dotfile: keep the whole name as the stem
		stem, ext = base, ""
	}
	ts := time.Unix(0, mtime).UTC().Format("20060102-150405")
	return fmt.Sprintf("%s%s.conflict-%s-%s-%s%s", dir, stem, unsafeName.ReplaceAllString(machine, "_"), ts, hash[:min(6, len(hash))], ext)
}
