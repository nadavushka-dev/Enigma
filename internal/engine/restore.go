package engine

import (
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
// on disk, and nothing is in its way.
func (e *Engine) needsRestore(entry, rel string) bool {
	fs := e.st.Files[rel]
	if fs == nil || fs.V.IsDeleted() || fs.Materialized || e.shadowed(entry, rel) {
		return false
	}
	info, err := fsx.Stat(e.abs(rel))
	return err == nil && info.Kind == fsx.Missing
}

// requestRestore asks every linked peer that shares rel for its current
// version; onFile writes it back (see restoreDecision).
func (e *Engine) requestRestore(rel string) {
	if t, ok := e.restoreAsked[rel]; ok && time.Since(t) < restoreEvery {
		return
	}
	asked := false
	for _, p := range e.peers {
		if e.sharesPath(rel, p.id) {
			p.send(&proto.Msg{Type: proto.Fetch, Path: rel})
			asked = true
		}
	}
	if asked {
		e.restoreAsked[rel] = time.Now()
		e.log.Info("restoring a path that is no longer behind a link", "path", rel)
	}
}

// restoreShadowIn fetches the paths under rel that peers changed while
// they were shadowed here, now that they are not.
func (e *Engine) restoreShadowIn(entry, rel string) {
	for p := range e.shadowIn {
		if (p == rel || strings.HasPrefix(p, rel+"/")) && !e.shadowed(entry, p) {
			delete(e.shadowIn, p)
			e.requestRestore(p)
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
