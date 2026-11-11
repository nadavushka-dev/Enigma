package engine

import (
	"sort"
	"strings"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/proto"
	"github.com/nadavushka-dev/enigma/internal/version"
)

// Restore, never delete (SPEC §6.6). A tracked path found under a
// symlinked folder of its entry is not this machine's copy any more: its
// version is kept but no longer marked as on disk here (Materialized).
// When the link goes away, the path's absence is therefore not a delete;
// the version is fetched again from the peers and written back. Changes a
// peer sent for a shadowed path while it was shadowed are remembered
// (shadowIn, in memory) and fetched the same way.

// restoreEvery bounds how often one path is asked for again.
const restoreEvery = 30 * time.Second

// needsRestore: rel has a live current version this machine does not have
// on disk, and nothing is in its way. The entry's root must be here: a
// restore never re-creates a root the user removed (§6.5).
func (e *Engine) needsRestore(entry, rel string) bool {
	fs := e.st.Files[rel]
	if fs == nil || fs.V.IsDeleted() || fs.Materialized || e.missing[entry] ||
		!e.rootPresent(entry, e.st.Entries[entry]) || e.shadowed(entry, rel) {
		return false
	}
	info, err := fsx.Stat(e.abs(rel))
	return err == nil && info.Kind == fsx.Missing
}

// requestRestore asks one linked peer that shares rel for its current
// version; onFile writes it back (see restoreDecision). The peer is the
// one that just announced rel (it has a version of it), else the next in
// turn: each later attempt asks another, so one peer without it does not
// stall the restore. Nothing is asked while the entry's root is missing.
func (e *Engine) requestRestore(entry, rel string) {
	if e.missing[entry] || !e.rootPresent(entry, e.st.Entries[entry]) {
		return
	}
	if t, ok := e.restoreAsked[rel]; ok && time.Since(t) < restoreEvery {
		return
	}
	p := e.restoreFrom
	if p == nil || !e.sharesPath(rel, p.id) {
		var ids []string
		for id, q := range e.peers {
			if e.sharesPath(rel, q.id) {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return
		}
		sort.Strings(ids)
		p = e.peers[ids[e.restoreTries[rel]%len(ids)]]
	}
	e.restoreTries[rel]++
	p.send(&proto.Msg{Type: proto.Fetch, Path: rel})
	e.restoreAsked[rel] = time.Now()
	e.log.Info("restoring a path that is no longer behind a link", "path", rel, "from", p.name)
}

// restoreShadowIn fetches the paths under rel that peers changed while
// they were shadowed here, now that they are not.
func (e *Engine) restoreShadowIn(entry, rel string) {
	if e.missing[entry] || !e.rootPresent(entry, e.st.Entries[entry]) {
		return // kept for when the root is back
	}
	for p := range e.shadowIn {
		if (p == rel || strings.HasPrefix(p, rel+"/")) && !e.shadowed(entry, p) {
			delete(e.shadowIn, p)
			e.requestRestore(entry, p)
		}
	}
}

// restoreDecision turns "old news" into a write when the incoming version
// is exactly this machine's current one and it is missing here because it
// was behind a link (Decide only looks at versions, not at the disk).
func (e *Engine) restoreDecision(entry, rel string, in version.Version, d Decision) Decision {
	cur := e.current(rel)
	if d.Action != Ignore || cur == nil || !sameContent(in, *cur) ||
		version.Compare(in.Vector, cur.Vector) != version.Equal || !e.needsRestore(entry, rel) {
		return d
	}
	return Decision{Action: Apply, Result: *cur}
}
