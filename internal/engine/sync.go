package engine

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/merge"
	"github.com/nadavushka-dev/enigma/internal/proto"
	"github.com/nadavushka-dev/enigma/internal/trash"
	"github.com/nadavushka-dev/enigma/internal/version"
)

const maxApplyAttempts = 3

// afterDecide is a test hook run between a decision and its execution,
// where a concurrent user edit would race the engine.
var afterDecide atomic.Pointer[func(machine, rel string, act Action)]

// onFetchHook is a test hook run when a peer asks this machine for rel.
var onFetchHook atomic.Pointer[func(machine, rel string)]

// dropFetchHook is a test hook: when it returns true, this machine does
// not answer a peer asking for rel (a peer that does not have it).
var dropFetchHook atomic.Pointer[func(machine, rel string) bool]

// ---- entries (fleet state, SPEC §6.4) ----------------------------------------

func (e *Engine) addEntry(abs string, o AddOptions) error {
	ignore, group, policy, byPath := o.Ignore, o.Group, o.Policy, o.ByPath
	rel, ok := e.rel(abs)
	if !ok {
		return fmt.Errorf("%s: only paths inside your home folder can be synced", abs)
	}
	if group == "" {
		group = proto.MainGroup
	}
	g, gok := e.st.Groups[group]
	switch {
	case !gok || g.Removed:
		return fmt.Errorf("no group %q (enigma group list shows them)", group)
	case !e.inGroup(g, e.o.MachineID):
		return fmt.Errorf("this machine is not a member of group %s; join it first (enigma group add %s %s)", g.Name, g.Name, e.o.Name)
	}
	info, err := fsx.Stat(abs)
	if err != nil {
		return err
	}
	switch info.Kind {
	case fsx.Missing:
		return fmt.Errorf("%s does not exist", abs)
	case fsx.Other:
		return fmt.Errorf("%s is not a regular file or folder (symlinks are not synced)", abs)
	}
	for path, r := range e.st.Entries {
		if r.Removed {
			continue
		}
		switch {
		case path == rel:
			return fmt.Errorf("%s is already synced in %s; use enigma entry move", abs, e.groupName(r.Group))
		case r.Dir && strings.HasPrefix(rel, path+"/"):
			return fmt.Errorf("%s is inside the synced folder ~/%s", abs, path)
		case strings.HasPrefix(path, rel+"/"):
			return fmt.Errorf("%s contains the synced entry ~/%s; remove that first", abs, path)
		}
	}
	dir := info.Kind == fsx.Dir
	if o.Links && !dir {
		return fmt.Errorf("%s is a file; link replication is for folders", abs)
	}
	if dir {
		lim := e.lim()
		n, err := fsx.CountSynced(abs, fsx.NewMatcher(ignore), lim.MaxFileSize, lim.MaxFiles, o.Links)
		if err != nil {
			return err
		}
		if n > lim.MaxFiles {
			return fmt.Errorf("%s has more than %s files, the limit for a folder (Enigma is not for whole projects; raise it with enigma limits --files N)", abs, FormatCount(lim.MaxFiles))
		}
	}
	policy, byPath, err = checkFirstAddPolicy(dir, policy, byPath)
	if err != nil {
		return err
	}
	e.st.Lamport++
	rec := proto.EntryRecord{Path: rel, Dir: dir, Ignore: ignore, Group: group, Clock: e.st.Lamport, By: e.o.MachineID,
		FirstAdd: policy, FirstAddByPath: byPath, Links: o.Links}
	e.applyEntry(rec, nil)
	return nil
}

// checkFirstAddPolicy validates and normalizes the first-add choice (D22).
func checkFirstAddPolicy(dir bool, policy proto.FirstAddPolicy, byPath map[string]proto.FirstAddPolicy) (proto.FirstAddPolicy, map[string]proto.FirstAddPolicy, error) {
	switch policy {
	case "", proto.FirstAddKeepBoth:
		policy = proto.FirstAddKeepBoth
	case proto.FirstAddReplace:
	default:
		return "", nil, fmt.Errorf("unknown first-add choice %q (keep-both, mine-wins or cancel)", policy)
	}
	if len(byPath) == 0 {
		return policy, nil, nil
	}
	if !dir {
		return "", nil, fmt.Errorf("per-file first-add choices need a folder")
	}
	out := make(map[string]proto.FirstAddPolicy, len(byPath))
	for rel, p := range byPath {
		if rel == "" || path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") || strings.HasSuffix(rel, "/") {
			return "", nil, fmt.Errorf("bad first-add path %q", rel)
		}
		switch p {
		case proto.FirstAddKeepBoth, proto.FirstAddReplace:
			out[rel] = p
		default:
			return "", nil, fmt.Errorf("unknown first-add choice %q for %s", p, rel)
		}
	}
	return policy, out, nil
}

// applyEntry merges a record (local when from is nil) into fleet state.
func (e *Engine) applyEntry(r proto.EntryRecord, from *peer) {
	if r.Group == "" {
		r.Group = proto.MainGroup
	}
	old, had := e.st.Entries[r.Path]
	if from != nil && !had && !e.shares(r, from.id) {
		return // not ours to know (SPEC §9), or a peer with a stale view
	}
	if had && !r.Newer(old) {
		return
	}
	if r.Clock > e.st.Lamport {
		e.st.Lamport = r.Clock
	}
	e.st.Entries[r.Path] = r
	e.dirty = true
	// Links elsewhere may lead into this entry (D23).
	defer e.relinkOthers(r.Path)
	// Peers in the entry's group get it; so do peers that shared its old
	// group, so a moved entry freezes where it no longer belongs. A peer
	// that newly shares it also gets the entry's current versions, as it
	// would on link-up (§6.5), since it missed every announcement so far.
	for _, p := range e.peers {
		if p == from {
			continue
		}
		now, before := e.shares(r, p.id), had && !old.Removed && e.shares(old, p.id)
		if now || before {
			p.send(&proto.Msg{Type: proto.Entries, Entries: []proto.EntryRecord{r}})
		}
		if now && !before && !r.Removed {
			e.sendEntrySummary(p, r.Path)
		}
	}

	wasActive := had && !old.Removed
	wasLive := wasActive && e.inScope(old)
	nowLive := !r.Removed && e.inScope(r)
	by := ""
	if from != nil {
		by = from.name
	}
	switch {
	case r.Removed && wasActive:
		e.event(EvEntryRemoved, r.Path, by, "")
	case !r.Removed && !wasActive:
		e.event(EvEntryAdded, r.Path, by, "")
	case !r.Removed && wasActive && old.Group != r.Group:
		e.event(EvEntryMoved, r.Path, by, "moved to group "+e.groupName(r.Group))
	}
	switch {
	case r.Removed:
		if wasLive {
			e.unwatch(r.Path)
		}
		if wasActive {
			e.forget(r.Path)
		}
	case nowLive && wasLive:
		e.track(r.Path, r)
		e.rescanEntry(r.Path)
	case nowLive: // entering scope (new here, or thawed)
		e.track(r.Path, r)
		if wasActive {
			e.rescanEntry(r.Path) // edits made while frozen become versions (§7)
			return
		}
		origin := version.Preexisting
		if from == nil && r.By == e.o.MachineID {
			origin = version.Seed
		}
		e.seedEntry(r.Path, r, origin)
	case wasLive: // leaving scope: stop syncing, keep files and versions
		e.unwatch(r.Path)
	}
}

// inScope: this machine is in r's group, so r is synced here.
func (e *Engine) inScope(r proto.EntryRecord) bool {
	g, ok := e.st.Groups[r.Group]
	return ok && !g.Removed && e.inGroup(g, e.o.MachineID)
}

// refreshScope watches the live entries this machine is in scope for and
// stops watching the rest, after a group change (SPEC §9).
func (e *Engine) refreshScope() {
	for path, r := range e.st.Entries {
		if r.Removed {
			continue
		}
		_, watched := e.matchers[path]
		switch {
		case e.inScope(r) && !watched:
			e.track(path, r)
			e.rescanEntry(path)
		case !e.inScope(r) && watched:
			e.unwatch(path)
		}
	}
}

// MoveEntry puts the entry at abs in another group (SPEC §9). This machine
// must be in both groups.
func (e *Engine) MoveEntry(abs string, group string) error {
	var err error
	if derr := e.do(func() {
		rel, ok := e.rel(abs)
		r, exists := e.st.Entries[rel]
		if !ok || !exists || r.Removed {
			err = fmt.Errorf("%s is not synced", abs)
			return
		}
		if !e.inScope(r) {
			err = fmt.Errorf("%s belongs to group %s, which this machine is not in", abs, e.groupName(r.Group))
			return
		}
		g, gerr := e.liveGroup(group)
		if gerr != nil {
			err = gerr
			return
		}
		if !e.inGroup(g, e.o.MachineID) {
			err = fmt.Errorf("this machine is not a member of group %s; join it first", g.Name)
			return
		}
		if r.Group == group {
			err = fmt.Errorf("%s is already in %s", abs, g.Name)
			return
		}
		e.st.Lamport++
		r.Clock, r.By, r.Group = e.st.Lamport, e.o.MachineID, group
		e.applyEntry(r, nil)
	}); derr != nil {
		return derr
	}
	return err
}

func (e *Engine) track(path string, r proto.EntryRecord) {
	m := fsx.NewMatcher(r.Ignore)
	e.matchers[path] = m
	e.rootSeen[path] = e.rootSig(path) // pollRoots compares with this
	e.checkRoot(path)                  // record the folder, or notice it is another one
	if e.w == nil {
		return
	}
	root := e.abs(path)
	skip := func(abs string) bool {
		rel, err := filepath.Rel(root, abs)
		return err == nil && m.Ignored(filepath.ToSlash(rel))
	}
	// Watching creates a missing root (D6, a brand-new entry). A root that
	// is gone while this machine has versions under it, a file entry's
	// parent included, or is not a folder, is not watched: it is missing
	// locally, and pollRoots watches it again once it is a folder (§6.5).
	// A file entry's parent may be a symlink (dotfiles): it is watched
	// through it as always.
	if dir := e.rootDir(path); !isFolder(dir) {
		fi, err := os.Lstat(dir)
		if err == nil && (r.Dir || fi.Mode()&os.ModeSymlink == 0) || err != nil && e.rootVanished(path) {
			return
		}
	}
	if err := e.w.Add(root, !r.Dir, skip); err != nil {
		e.log.Error("watch", "entry", path, "err", err)
	}
}

// isFolder: p is a folder, not a symlink to one.
func isFolder(p string) bool {
	_, ok := dirIDOf(p)
	return ok
}

// unwatch stops watching an entry; its versions stay (a frozen entry).
func (e *Engine) unwatch(path string) {
	if e.w != nil {
		e.w.Remove(e.abs(path))
	}
	delete(e.matchers, path)
	delete(e.skipped, path)
	delete(e.overLimit, path)
	delete(e.missing, path)
	delete(e.watchRetry, path)
	e.clearLinkNotes(path, false)
}

// forget drops an entry's versions and stamps (it was removed).
func (e *Engine) forget(path string) {
	for rel := range e.st.Files {
		if rel == path || strings.HasPrefix(rel, path+"/") {
			delete(e.st.Files, rel)
		}
	}
	for rel := range e.st.Stamps {
		if rel == path || strings.HasPrefix(rel, path+"/") {
			delete(e.st.Stamps, rel)
		}
	}
	delete(e.st.Roots, path)
	delete(e.st.Unreliable, path)
	e.dirty = true
}

// seedEntry records the files this machine has for a newly active entry.
// On the adding machine they are Seed versions; elsewhere Preexisting (D7).
// Past the folder's file limit, the rest wait as over the limit (§8).
func (e *Engine) seedEntry(path string, r proto.EntryRecord, origin version.Origin) {
	files := e.scanEntry(path, r, true)
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	room := e.room(path, r)
	for _, rel := range rels {
		if fs := e.st.Files[rel]; fs != nil && !fs.V.IsDeleted() {
			continue
		}
		if room != nil {
			if *room <= 0 {
				e.markOverLimit(path, rel)
				continue
			}
			*room--
		}
		e.newLocalVersion(rel, files[rel], origin)
	}
	e.reportOverLimit(path, 0)
}

// scanEntry returns the syncable files of an entry, keyed by path from ~,
// and refreshes the entry's skipped list. Without hash, the infos carry no
// hash and no file is read (for the skipped list alone).
func (e *Engine) scanEntry(path string, r proto.EntryRecord, hash bool) map[string]fsx.Info {
	out := map[string]fsx.Info{}
	skipped := map[string]bool{}
	if r.Dir {
		root := e.abs(path)
		stat := func(p string) (fsx.Info, error) {
			if !hash {
				return fsx.Info{Kind: fsx.Regular}, nil // ScanWith checked the size already
			}
			rel, _ := filepath.Rel(root, p)
			return e.statFile(path + "/" + filepath.ToSlash(rel))
		}
		res, err := fsx.ScanWith(root, e.matchers[path], e.lim().MaxFileSize, stat)
		if err != nil {
			e.log.Error("scan", "entry", path, "err", err)
		}
		for f, info := range res.Files {
			out[path+"/"+f] = info
		}
		for _, f := range res.Skipped {
			skipped[path+"/"+f] = true
		}
		e.clearLinkNotes(path, true)
		if r.Links {
			for _, f := range res.Links {
				rel := path + "/" + f
				info, err := fsx.Stat(e.abs(rel))
				if err != nil || !info.Link {
					continue
				}
				li, stored, reason := e.linkInfo(path, rel, info)
				if reason != "" {
					e.noteLink(path, rel, stored, LinkSkipped, reason)
					continue
				}
				delete(skipped, rel)
				if hash {
					out[rel] = li
				}
			}
		}
	} else {
		info, _ := fsx.Stat(e.abs(path))
		if hash {
			info, _ = e.statFile(path)
		}
		switch {
		case info.Kind == fsx.Regular && info.Size > e.lim().MaxFileSize, info.Kind == fsx.Other:
			skipped[path] = true
		case info.Kind == fsx.Regular:
			out[path] = info
		}
	}
	e.skipped[path] = skipped
	return out
}

// ---- local changes (SPEC §6.1, §6.5) -------------------------------------------

func (e *Engine) rescanAll() {
	for path, r := range e.st.Entries {
		if !r.Removed && e.inScope(r) {
			e.rescanEntry(path)
		}
	}
}

func (e *Engine) rescanEntry(path string) {
	r := e.st.Entries[path]
	if e.rootPresent(path, r) {
		delete(e.missing, path)
	}
	e.scanEntry(path, r, false)
	e.refreshUnder(path, path)
}

// onLocalPath handles one watcher-reported path.
func (e *Engine) onLocalPath(abs string) {
	rel, ok := e.rel(abs)
	if !ok {
		return
	}
	if entry, r, ok := e.entryFor(rel); ok {
		if rel == entry && r.Dir {
			// The watcher reports a folder root only when it is deleted or
			// moved away: a folder here later is another one (§6.5).
			e.log.Info("entry folder deleted or moved", "entry", entry)
			e.rootGone(entry)
			e.rewatch(entry)
		}
		e.refreshUnder(entry, rel)
		return
	}
	for path, r := range e.st.Entries { // an ancestor of entries changed
		if !r.Removed && strings.HasPrefix(path, rel+"/") {
			e.rescanEntry(path)
		}
	}
}

// refreshUnder refreshes rel and, if it is or was a folder, everything under it.
// New files past the folder's file limit wait as over the limit (§8), and
// the ones waiting take any room this refresh made.
func (e *Engine) refreshUnder(entry, rel string) {
	if rel == entry {
		e.rootSeen[entry] = e.rootSig(entry) // what pollRoots compares with
		switch {
		case e.rootIsLink(entry):
			// Nothing under a linked root is this entry's (§6.6): no
			// writes, no deletes, until it is a folder again (§6.5 guard).
			e.markMissingWhy(entry, "the folder is a symlink here now; nothing is synced through it until it is a folder again",
				"~/"+entry+" is a symlink now; nothing is synced through it until it is a folder again")
		case e.rootPresent(entry, e.st.Entries[entry]):
			delete(e.missing, entry) // back, as rescanEntry finds it (a file event is enough)
		}
	}
	e.checkRoot(entry) // another folder than the one synced: restore, never delete
	waiting := len(e.overLimit[entry])
	room := e.room(entry, e.st.Entries[entry])
	seen := map[string]bool{}
	if info, _ := fsx.Stat(e.abs(rel)); info.Kind == fsx.Dir {
		// The entry's own ignores apply from its root; below it, the
		// defaults (refreshFile checks the entry's ignores on each path).
		m := fsx.NewMatcher(nil)
		if em := e.matchers[entry]; em != nil && rel == entry {
			m = em
		}
		files, _ := fsx.ListFiles(e.abs(rel), m)
		if e.linksOn(entry) {
			links, _ := fsx.ListLinks(e.abs(rel), m)
			files = append(files, links...)
		}
		for _, f := range files {
			p := path.Join(rel, f)
			seen[p] = true
			e.refreshFile(entry, p, room)
		}
	} else {
		seen[rel] = true
		e.refreshFile(entry, rel, room)
	}
	for p := range e.st.Files {
		if !seen[p] && strings.HasPrefix(p, rel+"/") {
			e.refreshFile(entry, p, room)
		}
	}
	if over := e.overLimit[entry]; len(over) > 0 { // room freed, or gone from disk
		waitingNow := make([]string, 0, len(over))
		for p := range over {
			waitingNow = append(waitingNow, p)
		}
		sort.Strings(waitingNow)
		for _, p := range waitingNow {
			e.refreshFile(entry, p, room)
		}
	}
	e.restoreShadowIn(entry, rel)
	e.reportOverLimit(entry, waiting)
}

// rootIsLink: entry is a folder entry whose root is a symlink.
func (e *Engine) rootIsLink(entry string) bool {
	if r := e.st.Entries[entry]; !r.Dir {
		return false
	}
	fi, err := os.Lstat(e.abs(entry))
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// room is how many more files a folder entry may track (nil: no limit,
// a single-file entry). It may be negative: files received from peers
// count too, and a limit can be lowered.
func (e *Engine) room(entry string, r proto.EntryRecord) *int {
	if !r.Dir {
		return nil
	}
	n := e.lim().MaxFiles
	for p, fs := range e.st.Files {
		if !fs.V.IsDeleted() && strings.HasPrefix(p, entry+"/") {
			n--
		}
	}
	return &n
}

func (e *Engine) markOverLimit(entry, rel string) {
	over := e.overLimit[entry]
	if over == nil {
		over = map[string]bool{}
		e.overLimit[entry] = over
	}
	over[rel] = true
}

// reportOverLimit tells the user once that entry went over its file limit,
// given how many files were waiting before.
func (e *Engine) reportOverLimit(entry string, before int) {
	n := len(e.overLimit[entry])
	switch {
	case n > 0 && before == 0:
		files := "files"
		if n == 1 {
			files = "file"
		}
		msg := fmt.Sprintf("over the %s-file limit: %d new %s not synced", FormatCount(e.lim().MaxFiles), n, files)
		e.log.Warn("folder over its file limit", "entry", entry, "waiting", n)
		e.event(EvError, entry, "", msg)
		e.notify(entry, "~/"+entry+" is "+msg)
	case n == 0 && before > 0:
		e.log.Info("folder back under its file limit", "entry", entry)
	}
	if n == 0 {
		delete(e.overLimit, entry)
	}
}

// refreshFile makes the current version of rel match the disk, creating a
// local version (edit or delete) if it does not. With room, a new file
// takes one place, or waits as over the limit when there is none; a
// delete frees one.
func (e *Engine) refreshFile(entry, rel string, room *int) {
	if e.ignored(entry, rel) {
		return
	}
	if e.shadowed(entry, rel) { // not a delete either: the link hides it
		delete(e.overLimit[entry], rel)
		fs := e.st.Files[rel]
		if fs == nil || fs.V.IsDeleted() {
			delete(e.skipped[entry], rel) // nothing to sync there: not skipped
			return
		}
		e.markSkipped(entry, rel)
		if fs.Materialized {
			// What is there now is behind a link, not this version: once
			// the link goes, the path is restored, never deleted (§6.6).
			fs.Materialized, e.dirty = false, true
		}
		return
	}
	fs := e.st.Files[rel]
	isNew := fs == nil || fs.V.IsDeleted()
	if room != nil && isNew && *room <= 0 {
		// Do not read a file that cannot be synced yet.
		if st, ok := fsx.StampOf(e.abs(rel)); ok && st.Size <= e.lim().MaxFileSize {
			e.markOverLimit(entry, rel)
			return
		}
	}
	delete(e.overLimit[entry], rel)
	info, err := e.statFile(rel)
	if err != nil {
		e.log.Error("stat", "path", rel, "err", err)
		return
	}
	if n := e.linkNotes[rel]; n != nil && n.state == LinkSkipped && !info.Link {
		delete(e.linkNotes, rel) // not a link any more: nothing to report
	}
	sk := e.skipped[entry]
	if sk == nil {
		sk = map[string]bool{}
		e.skipped[entry] = sk
	}
	switch info.Kind {
	case fsx.Regular:
		if info.Size > e.lim().MaxFileSize {
			sk[rel] = true
			return
		}
		delete(sk, rel)
		if fs != nil && !fs.V.IsDeleted() && fs.V.Hash == info.Hash && fs.V.Exec == info.Exec {
			if !fs.Materialized {
				fs.Materialized, e.dirty = true, true
			}
			return
		}
		if room != nil && isNew {
			*room--
		}
		e.newLocalVersion(rel, info, version.Edit)
	case fsx.Missing:
		delete(sk, rel)
		if fs == nil || fs.V.IsDeleted() {
			return // never had it here: not a delete
		}
		if !e.rootPresent(entry, e.st.Entries[entry]) {
			e.markMissing(entry) // no deletes, and no restore re-creating the root
			return
		}
		if fs.V.Link && !e.linksOn(entry) {
			// Links off: links are not synced either way, so removing one
			// here is not a delete elsewhere (D23). It is not on disk here.
			if fs.Materialized {
				fs.Materialized, e.dirty = false, true
			}
			return
		}
		e.checkRoot(entry) // a replaced folder: nothing in it is a delete
		if !fs.Materialized {
			e.requestRestore(entry, rel) // not on disk here (it was behind a link): fetch it
			return
		}
		v := version.Version{Hash: version.Deleted, Vector: e.bump(fs.V.Vector), Parents: []string{fs.V.Hash},
			Mtime: time.Now().UnixNano(), By: e.o.MachineID, ByName: e.o.Name}
		e.log.Debug("local delete", "path", rel, "v", fmtVersion(&v))
		e.event(EvDeleted, rel, "", "")
		e.setCurrent(rel, v, false)
		e.announce(rel, v, nil)
		if room != nil {
			*room++
		}
	case fsx.Other:
		if info.Link && e.linksOn(entry) && rel != entry {
			e.refreshLink(entry, rel, info, room)
			return
		}
		sk[rel] = true
	}
}

// markMissing applies the vanished-root guard (§6.5) to entry.
func (e *Engine) markMissing(entry string) {
	e.markMissingWhy(entry, "the folder vanished here; deletes are not sent until it is back",
		"~/"+entry+" vanished; deletes are not sent until it is back")
}

// markMissingWhy is markMissing with the activity detail and the
// notification shown to the user.
func (e *Engine) markMissingWhy(entry, detail, note string) {
	if !e.missing[entry] {
		e.log.Warn("entry root missing; not propagating deletes", "entry", entry, "why", detail)
		// Activity only: the standing "missing locally" problem comes from
		// the status, so it clears when the folder is back.
		e.event(EvRootMissing, entry, "", detail)
		e.notify(entry, note)
	}
	e.missing[entry] = true
	e.rootGone(entry) // whatever comes back here is a replacement
}

// rootVanished: the entry's root is gone although this machine had files
// under it: on disk, or kept as not on disk here after a link hid them
// (§6.6). A brand-new entry's root is merely not created yet (D6).
func (e *Engine) rootVanished(entry string) bool {
	if e.rootPresent(entry, e.st.Entries[entry]) {
		return false
	}
	for rel, fs := range e.st.Files {
		if !fs.V.IsDeleted() && (rel == entry || strings.HasPrefix(rel, entry+"/")) {
			return true
		}
	}
	return false
}

func (e *Engine) newLocalVersion(rel string, info fsx.Info, origin version.Origin) {
	var vec version.Vector
	var parents []string
	if fs := e.st.Files[rel]; fs != nil {
		vec, parents = fs.V.Vector, []string{fs.V.Hash}
	}
	v := version.Version{Hash: info.Hash, Vector: e.bump(vec), Parents: parents, Mtime: info.Mtime,
		Exec: info.Exec, By: e.o.MachineID, ByName: e.o.Name, Origin: origin, Link: info.Link}
	e.log.Debug("local version", "path", rel, "v", fmtVersion(&v))
	if origin == version.Edit { // not every file of a new entry
		e.event(EvChanged, rel, "", "")
	}
	if !info.Link { // reading a link would follow it
		if data, err := os.ReadFile(e.abs(rel)); err == nil && fsx.Hash(data) == v.Hash {
			e.keep(data)
		}
	}
	e.setCurrent(rel, v, true)
	e.announce(rel, v, nil)
}

// bump advances this machine's counter in vec. The counter is global, so a
// re-added entry's new versions are always newer than anything this machine
// made before.
func (e *Engine) bump(vec version.Vector) version.Vector {
	e.st.Counter++
	out := version.Merge(vec, nil)
	out[e.o.MachineID] = e.st.Counter
	return out
}

// setCurrent makes v the current version of rel. Its ancestry grows by v
// and by what a peer sent about it (learned), trimmed to the nearest hashes.
func (e *Engine) setCurrent(rel string, v version.Version, materialized bool, learned ...version.Node) {
	g := version.Graph{}
	if old := e.st.Files[rel]; old != nil {
		g.Add(version.Walk(old.Anc, old.V.Hash, version.AncestryLimit))
	}
	g.Add(learned)
	g.Add([]version.Node{{Hash: v.Hash, Parents: v.Parents}})
	e.st.Files[rel] = &fileState{V: v, Materialized: materialized, Anc: version.Trim(g, v.Hash, version.AncestryLimit)}
	e.dirty = true
}

// keep stores content in the history store, as a future merge base.
func (e *Engine) keep(data []byte) {
	if _, err := e.hist.Put(data); err != nil {
		e.log.Error("history", "err", err)
	}
}

func (e *Engine) current(rel string) *version.Version {
	if fs := e.st.Files[rel]; fs != nil {
		v := fs.V
		return &v
	}
	return nil
}

// ---- remote changes (SPEC §6.3, §7) --------------------------------------------

// firstAddPolicy is the entry's choice for rel (D22); loop only.
func (e *Engine) firstAddPolicy(entry, rel string) proto.FirstAddPolicy {
	if r, ok := e.st.Entries[entry]; ok {
		return r.FirstAddFor(rel)
	}
	return proto.FirstAddKeepBoth
}

// accept reports the entry covering rel if remote changes to it may apply.
func (e *Engine) accept(rel string) (string, bool) {
	entry, _, ok := e.entryFor(rel)
	if !ok || e.ignored(entry, rel) {
		return "", false
	}
	if e.missing[entry] {
		if e.rootIsLink(entry) {
			e.shadowIn[rel] = true // fetched once the root is a folder again
		}
		return "", false
	}
	return entry, true
}

func (e *Engine) onAnnounce(p *peer, rel string, in version.Version) {
	entry, ok := e.accept(rel)
	if !ok || (in.Link || e.isLink(rel)) && !e.linksOn(entry) {
		return // links off: neither created nor deleted here (D23)
	}
	e.restoreFrom = p // a restore this refresh asks for goes to p, which has a version of rel
	e.refreshFile(entry, rel, nil)
	e.restoreFrom = nil
	d := DecidePolicy(rel, e.current(rel), in, false, e.firstAddPolicy(entry, rel))
	if err := e.execute(p, entry, rel, in, nil, nil, d); err != nil && !errors.Is(err, fsx.ErrChanged) {
		e.log.Error("apply", "path", rel, "err", err)
		e.event(EvError, rel, p.name, err.Error())
		e.notify(rel, "could not write ~/"+rel+": "+err.Error())
	}
}

func (e *Engine) onFetch(p *peer, rel string) {
	if hook := onFetchHook.Load(); hook != nil {
		(*hook)(e.o.Name, rel)
	}
	if hook := dropFetchHook.Load(); hook != nil && (*hook)(e.o.Name, rel) {
		return
	}
	entry, _, ok := e.entryFor(rel)
	if !ok {
		return
	}
	for attempt := 0; attempt < maxApplyAttempts; attempt++ {
		e.refreshFile(entry, rel, nil)
		fs := e.st.Files[rel]
		if fs == nil {
			return
		}
		v := fs.V
		anc := version.Walk(fs.Anc, v.Hash, version.AncestryLimit)
		if v.IsDeleted() {
			p.send(&proto.Msg{Type: proto.File, Path: rel, Version: &v, Ancestry: anc})
			return
		}
		if !fs.Materialized || e.shadowed(entry, rel) {
			// Not on disk here (behind a link, a replaced folder): the
			// history store may still hold it, so a peer restoring it gets
			// it. Nothing is read through a link.
			if data, ok := e.hist.Get(v.Hash); ok && !v.Link && fsx.Hash(data) == v.Hash {
				p.send(&proto.Msg{Type: proto.File, Path: rel, Version: &v, Ancestry: anc, Content: data})
			}
			return
		}
		if v.Link { // the content is the stored target; never read through the link
			info, err := fsx.Stat(e.abs(rel))
			if err == nil && info.Link {
				if stored, _, err := storeLinkTarget(e.homes(), rel, info.Target); err == nil && fsx.LinkHash(stored) == v.Hash {
					p.send(&proto.Msg{Type: proto.File, Path: rel, Version: &v, Ancestry: anc, Content: []byte(stored)})
					return
				}
			}
			continue
		}
		data, err := os.ReadFile(e.abs(rel))
		if err == nil && fsx.Hash(data) == v.Hash {
			p.send(&proto.Msg{Type: proto.File, Path: rel, Version: &v, Ancestry: anc, Content: data})
			return
		}
		// Changed between refresh and read: refresh again.
	}
}

func (e *Engine) onFile(p *peer, rel string, in version.Version, anc []version.Node, content []byte) {
	entry, ok := e.accept(rel)
	if !ok {
		return
	}
	if e.isLink(rel) && !e.linksOn(entry) {
		return // links off: a link here is neither replaced nor deleted (D23)
	}
	linkTarget := "" // from ~, for a link version
	if in.Link {
		if !e.linksOn(entry) {
			return
		}
		stored := string(content)
		if fsx.LinkHash(stored) != in.Hash {
			e.log.Error("link target does not match its hash; dropped", "path", rel, "peer", p.name)
			return
		}
		// Checked again here: a peer is trusted but may be buggy (D23).
		target, ok := resolveStoredTarget(rel, stored)
		reason := "target is invalid or outside your home folder"
		if ok {
			reason = e.linkReason(entry, rel, target)
		}
		if reason != "" {
			e.log.Warn("received link not replicated", "path", rel, "peer", p.name, "reason", reason)
			e.markSkipped(entry, rel)
			e.noteLink(entry, rel, stored, LinkSkipped, reason)
			return
		}
		linkTarget = target
	} else if !in.IsDeleted() && fsx.Hash(content) != in.Hash {
		e.log.Error("content does not match its hash; dropped", "path", rel, "peer", p.name)
		return
	}
	if max := e.lim().MaxFileSize; !in.IsDeleted() && !in.Link && int64(len(content)) > max {
		msg := fmt.Sprintf("not received: %s, over the %s file limit", FormatSize(int64(len(content))), FormatSize(max))
		e.log.Warn("received file over the size limit; dropped", "path", rel, "peer", p.name, "size", len(content))
		e.event(EvError, rel, p.name, msg)
		e.notify(rel, "~/"+rel+" "+msg)
		return
	}
	if !in.IsDeleted() && !in.Link {
		e.keep(content)
	}
	delete(e.pending, rel) // superseded by this version, or applied now
	if n := e.linkNotes[rel]; n != nil && n.state == LinkPending {
		delete(e.linkNotes, rel)
	}
	for attempt := 0; attempt < maxApplyAttempts; attempt++ {
		e.refreshFile(entry, rel, nil)
		cur := e.current(rel)
		d := DecidePolicy(rel, cur, in, true, e.firstAddPolicy(entry, rel))
		if m, ok := e.tryMerge(rel, cur, in, anc, content); ok {
			d = DecideMerged(rel, cur, in, m)
		}
		d = e.restoreDecision(entry, rel, in, d)
		if d.Action == Apply && d.Result.Link {
			if _, err := os.Lstat(e.abs(linkTarget)); err != nil {
				// Never a dangling link: wait for the target (D23). Not a
				// delete either; nothing was written.
				e.pending[rel] = &pendingLink{p: p, in: in, anc: anc, content: content, target: linkTarget}
				e.noteLink(entry, rel, string(content), LinkPending, "waiting for its target ~/"+linkTarget)
				return
			}
		}
		err := e.execute(p, entry, rel, in, anc, content, d)
		if !errors.Is(err, fsx.ErrChanged) {
			if err != nil {
				e.log.Error("apply", "path", rel, "err", err)
				e.event(EvError, rel, p.name, err.Error())
				e.notify(rel, "could not write ~/"+rel+": "+err.Error())
			}
			return
		}
	}
}

// tryMerge three-way merges concurrent text edits on their common base
// (SPEC §7 step 3). It fails, sending Decide to the fallback, when there is
// no base, its content is not in history, a side is not text, or the edits
// overlap.
func (e *Engine) tryMerge(rel string, cur *version.Version, in version.Version, anc []version.Node, content []byte) (Merged, bool) {
	if cur == nil || cur.IsDeleted() || in.IsDeleted() || cur.Hash == in.Hash || cur.Link || in.Link ||
		version.Compare(in.Vector, cur.Vector) != version.Concurrent {
		return Merged{}, false
	}
	fs := e.st.Files[rel]
	base, ok := version.CommonBase(version.Walk(fs.Anc, cur.Hash, version.AncestryLimit), anc)
	if !ok {
		return Merged{}, false
	}
	baseData, ok := e.hist.Get(base)
	if !ok {
		e.log.Debug("merge base not in history", "path", rel, "base", base)
		return Merged{}, false
	}
	local, ok := e.hist.Get(cur.Hash)
	if !ok {
		return Merged{}, false
	}
	if !merge.IsText(baseData) || !merge.IsText(local) || !merge.IsText(content) {
		return Merged{}, false
	}
	out, ok := merge.Merge3(baseData, local, content)
	if !ok {
		return Merged{}, false
	}
	e.log.Info("merged concurrent edits", "path", rel)
	return Merged{Hash: fsx.Hash(out), Content: out}, true
}

func (e *Engine) execute(p *peer, entry, rel string, in version.Version, anc []version.Node, content []byte, d Decision) error {
	e.log.Debug("decide", "path", rel, "peer", p.name, "action", d.Action, "cur", fmtVersion(e.current(rel)),
		"in", fmtVersion(&in), "content", content != nil, "backup", d.Backup, "copy", d.Copy != nil)
	if hook := afterDecide.Load(); hook != nil {
		(*hook)(e.o.Name, rel, d.Action)
	}
	// The source already has the incoming version; anything else is news to it.
	announce := func(v version.Version) {
		skip := p
		if v.Hash != in.Hash || version.Compare(v.Vector, in.Vector) != version.Equal {
			skip = nil
		}
		e.announce(rel, v, skip)
	}
	abs := e.abs(rel)
	if (d.Action == Apply || d.Action == Delete || d.Action == Record && d.Copy != nil) && e.shadowed(entry, rel) {
		// The path leads through a symlinked folder: writing, trashing or
		// copying there would change what the link points at.
		e.log.Warn("not writing through a symlinked folder", "path", rel)
		if d.Action == Delete {
			// Nothing here is touched; the delete is only recorded, so the
			// path is not restored or listed as skipped later.
			e.setCurrent(rel, d.Result, false, anc...)
			delete(e.skipped[entry], rel)
			return nil
		}
		e.markSkipped(entry, rel)
		e.shadowIn[rel] = true // fetched once it is not behind a link (restore.go)
		return nil
	}
	switch d.Action {
	case Ignore:
		return nil
	case Fetch:
		p.send(&proto.Msg{Type: proto.Fetch, Path: rel})
		return nil
	case Record:
		switch {
		case d.Copy != nil && d.Copy.FromIncoming && d.Copy.Version.Link:
			// Never a conflict-copy link (D23): the losing link is trashed
			// on the machine that has it.
			e.log.Info("incoming link lost; its machine keeps it in its trash", "path", rel, "peer", p.name)
		case d.Copy != nil && d.Copy.FromIncoming:
			e.writeCopy(*d.Copy, content)
		}
		fs := e.st.Files[rel]
		e.setCurrent(rel, d.Result, fs != nil && fs.Materialized, anc...)
		announce(d.Result)
		return nil
	case Apply:
		// Writing would re-create a vanished root, and the files that were
		// there would then read as deletes.
		if e.rootVanished(entry) {
			e.markMissing(entry)
			return nil
		}
		// The precondition is what the decision was based on, never a fresh
		// look at the disk: an edit made since then must not be overwritten.
		basis, onDisk, curLink := "", false, false
		if fs := e.st.Files[rel]; fs != nil && fs.Materialized && !fs.V.IsDeleted() {
			basis, onDisk, curLink = fs.V.Hash, true, fs.V.Link
		}
		expectLink := ""
		if curLink {
			// A link has no content to back up or copy: when it loses, or
			// a file replaces it, the link itself goes to the trash (D23).
			raw, err := e.linkOnDisk(rel, basis)
			if err != nil {
				return err
			}
			// It lost when the decision keeps it nowhere, or when the incoming
			// version is another machine's resolution of a conflict that this
			// link lost (two parents, this one among them): every machine that
			// has the losing link keeps it in its trash.
			lost := d.Backup || (d.Copy != nil && !d.Copy.FromIncoming) ||
				d.Result.Link && d.Result.Hash != basis && len(d.Result.Parents) == 2 && contains(d.Result.Parents, basis)
			if lost || !d.Result.Link {
				if err := e.trashLink(rel, raw); err != nil {
					return err
				}
				if lost {
					e.event(EvLinkLost, rel, p.name, raw)
				}
				basis, onDisk = "", false
			} else {
				expectLink = raw
			}
			d.Backup = false
			if d.Copy != nil && !d.Copy.FromIncoming {
				d.Copy = nil
			}
		}
		if d.Backup || (d.Copy != nil && !d.Copy.FromIncoming) {
			var local []byte
			if fs := e.st.Files[rel]; !onDisk && fs != nil && !fs.V.IsDeleted() && e.needsRestore(entry, rel) {
				// The local version is not on disk here (it was behind a
				// link): keep it from history, as it would be from disk.
				data, ok := e.hist.Get(fs.V.Hash)
				if !ok {
					e.log.Warn("local version not on disk nor in history; nothing to keep", "path", rel)
					d.Backup, d.Copy = false, nil
				}
				local = data
			} else {
				data, err := os.ReadFile(abs)
				if err != nil {
					return err
				}
				if !onDisk || fsx.Hash(data) != basis {
					return fsx.ErrChanged
				}
				local = data
			}
			if d.Backup {
				if err := e.backup(rel, local); err != nil {
					return err
				}
			}
			if d.Copy != nil {
				e.writeCopy(*d.Copy, local)
			}
		}
		if d.Content != nil {
			content = d.Content
			e.keep(content)
		}
		if d.Result.Link {
			lo := fsx.LinkOpts{ExpectAbsent: !onDisk, ExpectHash: basis, ExpectLink: expectLink}
			if expectLink != "" {
				lo.ExpectHash = ""
			}
			if err := fsx.WriteLink(abs, materializeLinkTarget(e.givenHome, string(content)), lo); err != nil {
				if errors.Is(err, fsx.ErrNotRegular) { // never clobber a folder (§6.6)
					e.log.Warn("not replacing a folder with a link", "path", rel)
					e.noteLink(entry, rel, string(content), LinkBlocked, "a folder (or something else that is not a file) is in the way here")
					// Applied once what is in the way is gone (retryPending).
					e.pending[rel] = &pendingLink{p: p, in: in, anc: anc, content: content, blocked: true}
					return nil
				}
				return err
			}
			delete(e.linkNotes, rel)
			delete(e.skipped[entry], rel)
		} else if _, err := fsx.Write(abs, content, fsx.WriteOpts{Mtime: d.Result.Mtime, Exec: d.Result.Exec, ExpectAbsent: !onDisk, ExpectHash: basis}); err != nil {
			if errors.Is(err, fsx.ErrNotRegular) {
				e.log.Warn("not overwriting a non-regular file", "path", rel)
				return nil
			}
			return err
		}
		e.checkRoot(entry) // records the folder this write made, or notices it is another
		e.setCurrent(rel, d.Result, true, anc...)
		announce(d.Result)
		if d.Content != nil {
			e.event(EvMerged, rel, p.name, "")
		} else {
			e.event(EvReceived, rel, p.name, "")
		}
		return nil
	case Delete:
		fs := e.st.Files[rel]
		info, err := fsx.Stat(abs)
		if err != nil {
			return err
		}
		if info.Link && fs != nil && fs.Materialized && !fs.V.IsDeleted() && fs.V.Link {
			// The link itself goes to the trash, never what it points at.
			raw, err := e.linkOnDisk(rel, fs.V.Hash)
			if err != nil {
				return err
			}
			if err := e.trashLink(rel, raw); err != nil {
				return err
			}
			fsx.RemoveEmptyDirs(filepath.Dir(abs), e.abs(entry))
			e.setCurrent(rel, d.Result, false, anc...)
			announce(d.Result)
			e.event(EvDeleted, rel, p.name, "")
			return nil
		}
		if info.Kind == fsx.Regular {
			if fs == nil || !fs.Materialized || fs.V.IsDeleted() {
				return fsx.ErrChanged // a file appeared since the decision
			}
			if _, err := trash.Move(filepath.Join(e.o.DataDir, "trash"), abs, rel, fs.V.Hash, time.Now()); err != nil {
				return err
			}
			if e.st.Entries[entry].Dir {
				fsx.RemoveEmptyDirs(filepath.Dir(abs), e.abs(entry))
			}
		}
		e.setCurrent(rel, d.Result, false, anc...)
		announce(d.Result)
		if info.Kind == fsx.Regular {
			e.event(EvDeleted, rel, p.name, "")
		}
		return nil
	}
	return fmt.Errorf("unknown action %v", d.Action)
}

// writeCopy creates a conflict copy (SPEC §7). Inside a folder entry it is
// tracked and syncs like any file.
func (e *Engine) writeCopy(c Copy, data []byte) {
	abs := e.abs(c.Path)
	info, err := fsx.Stat(abs)
	if err != nil {
		e.log.Error("conflict copy", "path", c.Path, "err", err)
		return
	}
	switch {
	case info.Kind == fsx.Regular && info.Hash == c.Version.Hash:
		// Already made, by us or by the peer resolving the same conflict.
	case info.Kind == fsx.Missing:
		if _, err := fsx.Write(abs, data, fsx.WriteOpts{Mtime: c.Version.Mtime, Exec: c.Version.Exec, ExpectAbsent: true}); err != nil {
			e.log.Error("conflict copy", "path", c.Path, "err", err)
			return
		}
	default:
		// The name is taken by other content (should not happen with the
		// hash in the name). Never drop the copy: use a numbered name.
		for i := 2; ; i++ {
			ext := path.Ext(c.Path)
			alt := fmt.Sprintf("%s.%d%s", strings.TrimSuffix(c.Path, ext), i, ext)
			if _, err := fsx.Write(e.abs(alt), data, fsx.WriteOpts{Mtime: c.Version.Mtime, Exec: c.Version.Exec, ExpectAbsent: true}); err == nil {
				e.log.Warn("conflict copy name taken; used another", "path", alt)
				c.Path = alt
				break
			} else if !errors.Is(err, fsx.ErrChanged) && !errors.Is(err, fsx.ErrNotRegular) {
				e.log.Error("conflict copy", "path", alt, "err", err)
				return
			}
		}
	}
	e.keep(data)
	e.log.Warn("conflict", "kept", c.Path)
	orig := c.Path
	if cc, ok := ParseConflictName(c.Path); ok {
		orig = cc.Original
	}
	if c.FirstAdd {
		// A first add can differ in many files: show it in Activity, but do
		// not flood the standing problems or the desktop notifications.
		e.event(EvKeptBoth, c.Path, "", orig)
	} else {
		e.event(EvConflict, c.Path, "", orig)
		e.notify(orig, "conflict on ~/"+orig+"; the other version is kept as ~/"+c.Path)
	}
	if entry, _, ok := e.entryFor(c.Path); ok && !e.ignored(entry, c.Path) {
		if cur := e.current(c.Path); cur == nil || version.Compare(c.Version.Vector, cur.Vector) == version.After {
			e.setCurrent(c.Path, c.Version, true)
			e.announce(c.Path, c.Version, nil)
		}
	}
}

// backup keeps a machine's own copy that lost a first add (D7, D13).
func (e *Engine) backup(rel string, data []byte) error {
	dst := e.dataPath("backups", rel+"."+time.Now().UTC().Format("20060102-150405"))
	for i := 2; ; i++ {
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			break
		}
		dst = fmt.Sprintf("%s.%d", strings.TrimSuffix(dst, fmt.Sprintf(".%d", i-1)), i)
	}
	if _, err := fsx.Write(dst, data, fsx.WriteOpts{ExpectAbsent: true}); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	e.log.Info("backed up local copy before first add", "path", rel, "backup", dst)
	return nil
}

// dataPath is <DataDir>/<kind>/<YYYY-MM-DD>/<rel> (backups).
func (e *Engine) dataPath(kind, rel string) string {
	return filepath.Join(e.o.DataDir, kind, time.Now().Format("2006-01-02"), filepath.FromSlash(rel))
}

func fmtVersion(v *version.Version) string {
	if v == nil {
		return "none"
	}
	h := v.Hash
	if len(h) > 6 {
		h = h[:6]
	}
	return fmt.Sprintf("%s%v/%s", h, map[string]uint64(v.Vector), v.Origin)
}

// stampSettle: a stamp younger than this is not trusted. On a file system
// with coarse timestamps, an edit right after the hash could keep the
// same size, mtime and ctime (git's "racy" files).
const stampSettle = 2 * time.Second

// statFile is fsx.Stat for rel that skips reading the file while its
// stamp matches the one taken with the last hash, and never reads a file
// over the size limit (its Info then has no hash).
func (e *Engine) statFile(rel string) (fsx.Info, error) {
	abs := e.abs(rel)
	st, ok := fsx.StampOf(abs)
	if !ok {
		e.dropStamp(rel)
		return fsx.Stat(abs) // missing, a folder or not regular: nothing to read
	}
	exec := st.Mode&0o111 != 0
	if st.Size > e.lim().MaxFileSize {
		e.dropStamp(rel)
		return fsx.Info{Kind: fsx.Regular, Size: st.Size, Mtime: st.Mtime, Exec: exec}, nil
	}
	if h := e.st.Stamps[rel]; h != nil && h.Stamp == st {
		return fsx.Info{Kind: fsx.Regular, Hash: h.Hash, Size: st.Size, Mtime: st.Mtime, Exec: exec}, nil
	}
	info, err := e.hashStat(abs)
	if err != nil || info.Kind != fsx.Regular {
		e.dropStamp(rel)
		return info, err
	}
	now := time.Now()
	after, ok := fsx.StampOf(abs)
	if ok && after == st && now.Sub(time.Unix(0, max(st.Mtime, st.Ctime))) > e.settle {
		e.st.Stamps[rel] = &hashStamp{Stamp: st, Hash: info.Hash}
		e.dirty = true
	} else {
		e.dropStamp(rel)
	}
	return info, nil
}

func (e *Engine) dropStamp(rel string) {
	if _, ok := e.st.Stamps[rel]; ok {
		delete(e.st.Stamps, rel)
		e.dirty = true
	}
}
