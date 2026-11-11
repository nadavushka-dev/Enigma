package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/proto"
	"github.com/nadavushka-dev/enigma/internal/version"
	"github.com/nadavushka-dev/enigma/internal/watch"
)

// Restore, never delete (SPEC §6.6). A tracked path found under a
// symlinked folder of its entry is not this machine's copy any more: its
// version is kept but no longer marked as on disk here (Materialized).
// When the link goes away, the path's absence is therefore not a delete;
// the version is fetched again from the peers and written back. Changes a
// peer sent for a shadowed path while it was shadowed are remembered
// (shadowIn, in memory) and fetched the same way.

// dirIDOf identifies a folder (fsx.DirIDOf); tests replace it to stand in
// for file systems that reuse inode numbers or change them.
var dirIDOf = fsx.DirIDOf

// rootDir is the folder entry's files live in: a folder entry's root, a
// file entry's parent.
func (e *Engine) rootDir(entry string) string {
	if e.st.Entries[entry].Dir {
		return e.abs(entry)
	}
	return filepath.Dir(e.abs(entry))
}

// checkRoot notices that entry's folder is not the one its files were
// synced in: moved away and re-created, replaced by another, a symlink
// in between. Nothing this machine had there is on disk as it left it, so
// none of it is marked on disk here any more: a file now missing is
// restored from the peers, never a delete (SPEC §6.5). The first folder
// seen is recorded. A symlink or an absent root is not a folder: nothing
// changes (the missing-root guard applies).
func (e *Engine) checkRoot(entry string) {
	id, ok := dirIDOf(e.rootDir(entry))
	if !ok {
		return
	}
	old, had := e.st.Roots[entry]
	if had && old == id {
		return
	}
	e.st.Roots[entry] = id
	e.dirty = true
	if !had || fsx.SameFolder(old, id) || e.st.Unreliable[entry] {
		return // first look, the device number drifting, or not relied on
	}
	// An identity that changes while every file is still in place is a
	// file system that does not keep it (FUSE, some network mounts):
	// after a few, across restarts, it is not relied on for this entry.
	if e.allOnDisk(entry) {
		e.st.Flips[entry]++
		if e.st.Flips[entry] >= rootFlipLimit {
			delete(e.st.Flips, entry)
			e.st.Unreliable[entry] = true
			e.log.Warn("entry folder identity keeps changing; not relying on it", "entry", entry)
			e.event(EvRootIdentity, entry, "", "the folder's identity keeps changing here (a network or FUSE file system?); a replaced folder is no longer told apart")
			return
		}
	}
	if n := e.notOnDiskAnyMore(entry); n > 0 {
		e.log.Warn("entry folder replaced; its files are restored from the peers, not deleted", "entry", entry, "files", n)
		e.event(EvRootReplaced, entry, "", "the folder was replaced here; missing files are restored from the other machines, never deleted")
	}
}

// rootFlipLimit: identity changes with every file in place before an
// entry's identity is not relied on any more.
const rootFlipLimit = 3

// allOnDisk: every file of entry marked on disk here is on disk.
func (e *Engine) allOnDisk(entry string) bool {
	n := 0
	for rel, fs := range e.st.Files {
		if fs.Materialized && !fs.V.IsDeleted() && (rel == entry || strings.HasPrefix(rel, entry+"/")) {
			if _, err := os.Lstat(e.abs(rel)); err != nil {
				return false
			}
			n++
		}
	}
	return n > 0
}

// notOnDiskAnyMore marks every file of entry as not on disk here (saved):
// what is at its folder now is not what this machine synced, so a file
// missing from it is restored, never a delete (§6.5). It returns how many.
func (e *Engine) notOnDiskAnyMore(entry string) int {
	n := 0
	for rel, fs := range e.st.Files {
		if fs.Materialized && !fs.V.IsDeleted() && (rel == entry || strings.HasPrefix(rel, entry+"/")) {
			fs.Materialized = false
			n++
		}
	}
	if n > 0 {
		e.dirty = true
	}
	return n
}

// rootGone: entry's folder was seen gone (absent, a symlink, deleted or
// moved away by a file event). Whatever folder is there later is a
// replacement, whatever its identity says (Linux reuses the inode of a
// deleted folder): nothing is on disk here any more, and the next folder
// seen is recorded afresh.
func (e *Engine) rootGone(entry string) {
	// A restore asked for before is moot (its answer finds no folder to
	// write in): when a folder is back, its files are asked for at once.
	for rel := range e.restoreAsked {
		if rel == entry || strings.HasPrefix(rel, entry+"/") {
			delete(e.restoreAsked, rel)
		}
	}
	if n := e.notOnDiskAnyMore(entry); n > 0 {
		e.log.Info("entry folder gone; its files are restored when it is back", "entry", entry, "files", n)
	}
	if _, ok := e.st.Roots[entry]; ok {
		delete(e.st.Roots, entry)
		e.dirty = true
	}
}

// rootSig is what is at entry's folder now: a folder (by identity), a
// symlink, something else, or nothing.
func (e *Engine) rootSig(entry string) string {
	dir := e.rootDir(entry)
	if id, ok := dirIDOf(dir); ok {
		return fmt.Sprintf("dir %d %d %d", id.Dev, id.Ino, id.Btime)
	}
	fi, err := os.Lstat(dir)
	switch {
	case err != nil:
		return "absent"
	case fi.Mode()&os.ModeSymlink != 0:
		return "link"
	}
	return "other"
}

// pollRoots notices an entry's folder moved away, replaced by a symlink,
// removed or re-created. File events do not report the watched folder
// itself reliably (kqueue follows links; a moved folder's watch follows
// it), so this runs with the link poll; it only stats each root. On a
// change the entry is watched again (or not, see track) and refreshed:
// missing, back, or restored (§6.5).
//
// An entry whose identity is not relied on (FUSE, some network mounts)
// is compared by what is there only: a folder, a symlink, something else
// or nothing; its identity changes on its own.
func (e *Engine) pollRoots() {
	health := e.watchHealth()
	for entry, r := range e.st.Entries {
		if r.Removed || !e.inScope(r) {
			continue
		}
		sig := e.rootSig(entry)
		old, had := e.rootSeen[entry]
		if !had {
			e.rootSeen[entry] = sig // first look; refreshUnder records it from then on
			continue
		}
		changed := old != sig
		if e.st.Unreliable[entry] {
			changed = sigKind(old) != sigKind(sig)
		}
		ws := e.rootWatch(entry, health)
		switch {
		case changed:
			e.log.Info("entry folder changed", "entry", entry, "was", old, "now", sig)
			delete(e.watchRetry, entry)
		case ws.dead:
			// Same identity, but the watch it had died with the folder
			// (deleted and re-created between two polls): another folder.
			e.log.Info("entry folder no longer watched; watching it again", "entry", entry)
			e.rootGone(entry)
			delete(e.watchRetry, entry)
		case ws.failed():
			e.retryWatch(entry)
			continue
		default:
			delete(e.watchRetry, entry)
			continue
		}
		e.rewatch(entry)
		e.refreshUnder(entry, entry)
	}
}

// sigKind is what kind of thing a rootSig is: dir, link, other or absent.
func sigKind(sig string) string {
	kind, _, _ := strings.Cut(sig, " ")
	return kind
}

// Retry bounds for a watch that could not be added (the inotify watch
// limit): retrying walks the entry's tree, so it backs off.
const (
	watchRetryMin = 4 * time.Second
	watchRetryMax = 5 * time.Minute
)

// retryWatch watches entry again when its retry is due, the first time a
// wait after the failure was seen. It never marks the files as not on
// disk: a watch that was never added did not die with its folder.
func (e *Engine) retryWatch(entry string) {
	now := time.Now()
	rt := e.watchRetry[entry]
	if rt == nil {
		e.watchRetry[entry] = &watchRetry{next: now.Add(watchRetryMin), wait: watchRetryMin}
		return
	}
	if now.Before(rt.next) {
		return
	}
	e.rewatch(entry)
	if ws := e.rootWatch(entry, e.watchHealth()); !ws.dead && !ws.failed() {
		delete(e.watchRetry, entry)
		e.log.Info("entry watched again", "entry", entry)
		e.refreshUnder(entry, entry) // changes made while it was not watched
		return
	}
	rt.wait = min(2*rt.wait, watchRetryMax)
	rt.next = now.Add(rt.wait)
	e.log.Warn("entry still not fully watched; trying again later", "entry", entry, "in", rt.wait)
}

// watchState is how an entry's folder is watched. dead: the watch it had
// died (the folder was deleted; the one there now is another). rootFailed:
// its watch could not be added (the watch limit), or it has none. folders:
// its subfolders (from ~) whose watch could not be added.
type watchState struct {
	dead, rootFailed bool
	folders          []string
}

func (s watchState) failed() bool { return s.rootFailed || len(s.folders) > 0 }

// watchHealth is every watched root's health, with one look at the
// watcher (nil without one).
func (e *Engine) watchHealth() map[string]watch.Health {
	if e.w == nil {
		return nil
	}
	return e.w.Health()
}

// rootWatch says how entry is watched, from health (watchHealth). Only an
// entry whose root folder (a file entry's parent) is a folder here is
// watched; anything else is the root poll's business. Only a folder
// entry's watch is known to die with its folder.
func (e *Engine) rootWatch(entry string, health map[string]watch.Health) watchState {
	if e.w == nil {
		return watchState{}
	}
	if _, ok := dirIDOf(e.rootDir(entry)); !ok {
		return watchState{}
	}
	dir := e.st.Entries[entry].Dir
	h, ok := health[e.abs(entry)]
	switch {
	case !ok:
		return watchState{dead: dir, rootFailed: !dir} // dropped: lost, as if it died
	case h.Added && !h.Alive:
		return watchState{dead: dir}
	}
	ws := watchState{rootFailed: !h.Added}
	for _, d := range h.Failed {
		if rel, ok := e.rel(d); ok && d != e.rootDir(entry) {
			ws.folders = append(ws.folders, rel)
		}
	}
	return ws
}

// watchRetry: a watch that could not be added is tried again at next,
// then after twice the wait, up to watchRetryMax.
type watchRetry struct {
	next time.Time
	wait time.Duration
}

// rewatch drops an entry's watch and watches it again (or not, see track).
func (e *Engine) rewatch(entry string) {
	if e.w == nil {
		return
	}
	e.w.Remove(e.abs(entry))
	e.track(entry, e.st.Entries[entry])
}

// DefaultRestoreRetry bounds how often one path is asked for again; each
// attempt asks the next peer.
const DefaultRestoreRetry = 30 * time.Second

// retryRestores asks again for the paths still waiting to be restored
// (the peer asked may not have had them), on the poll tick.
func (e *Engine) retryRestores() {
	for rel, entry := range e.restoring {
		if !e.needsRestore(entry, rel) {
			delete(e.restoring, rel)
			continue
		}
		e.requestRestore(entry, rel)
	}
}

// needsRestore: rel has a live current version this machine does not have
// on disk, and nothing is in its way. The entry's root must be here: a
// restore never re-creates a root the user removed (§6.5).
func (e *Engine) needsRestore(entry, rel string) bool {
	fs := e.st.Files[rel]
	if fs == nil || fs.V.IsDeleted() || fs.Materialized || e.missing[entry] || fs.V.Link && !e.linksOn(entry) ||
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
	e.restoring[rel] = entry
	if t, ok := e.restoreAsked[rel]; ok && time.Since(t) < e.o.RestoreRetry {
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
	e.log.Info("restoring a file not on disk here", "path", rel, "from", p.name)
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
