package engine

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
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

// ---- entries (group state, SPEC §6.4) ----------------------------------------

func (e *Engine) addEntry(abs string, ignore []string) error {
	rel, ok := e.rel(abs)
	if !ok {
		return fmt.Errorf("%s: only paths inside your home folder can be synced", abs)
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
			return fmt.Errorf("%s is already synced", abs)
		case r.Dir && strings.HasPrefix(rel, path+"/"):
			return fmt.Errorf("%s is inside the synced folder ~/%s", abs, path)
		case strings.HasPrefix(path, rel+"/"):
			return fmt.Errorf("%s contains the synced entry ~/%s; remove that first", abs, path)
		}
	}
	dir := info.Kind == fsx.Dir
	if dir {
		n, err := fsx.CountFiles(abs, fsx.NewMatcher(ignore), e.o.MaxFileSize, e.o.MaxFiles)
		if err != nil {
			return err
		}
		if n > e.o.MaxFiles {
			return fmt.Errorf("%s has more than %d files, the limit for a folder (Enigma is not for whole projects)", abs, e.o.MaxFiles)
		}
	}
	e.st.Lamport++
	e.applyEntry(proto.EntryRecord{Path: rel, Dir: dir, Ignore: ignore, Clock: e.st.Lamport, By: e.o.MachineID}, nil)
	return nil
}

// applyEntry merges a record (local when from is nil) into group state.
func (e *Engine) applyEntry(r proto.EntryRecord, from *peer) {
	old, had := e.st.Entries[r.Path]
	if had && !r.Newer(old) {
		return
	}
	if r.Clock > e.st.Lamport {
		e.st.Lamport = r.Clock
	}
	e.st.Entries[r.Path] = r
	e.dirty = true
	e.broadcast(&proto.Msg{Type: proto.Entries, Entries: []proto.EntryRecord{r}}, from)

	wasActive := had && !old.Removed
	by := ""
	if from != nil {
		by = from.name
	}
	switch {
	case r.Removed && wasActive:
		e.event(EvEntryRemoved, r.Path, by, "")
	case !r.Removed && !wasActive:
		e.event(EvEntryAdded, r.Path, by, "")
	}
	if r.Removed {
		if wasActive {
			e.untrack(r.Path)
		}
		return
	}
	e.track(r.Path, r)
	if wasActive {
		e.rescanEntry(r.Path)
		return
	}
	origin := version.Preexisting
	if from == nil && r.By == e.o.MachineID {
		origin = version.Seed
	}
	e.seedEntry(r.Path, r, origin)
}

func (e *Engine) track(path string, r proto.EntryRecord) {
	m := fsx.NewMatcher(r.Ignore)
	e.matchers[path] = m
	if e.w == nil {
		return
	}
	root := e.abs(path)
	skip := func(abs string) bool {
		rel, err := filepath.Rel(root, abs)
		return err == nil && m.Ignored(filepath.ToSlash(rel))
	}
	if err := e.w.Add(root, !r.Dir, skip); err != nil {
		e.log.Error("watch", "entry", path, "err", err)
	}
}

func (e *Engine) untrack(path string) {
	if e.w != nil {
		e.w.Remove(e.abs(path))
	}
	delete(e.matchers, path)
	delete(e.skipped, path)
	delete(e.missing, path)
	for rel := range e.st.Files {
		if rel == path || strings.HasPrefix(rel, path+"/") {
			delete(e.st.Files, rel)
		}
	}
	e.dirty = true
}

// seedEntry records the files this machine has for a newly active entry.
// On the adding machine they are Seed versions; elsewhere Preexisting (D7).
func (e *Engine) seedEntry(path string, r proto.EntryRecord, origin version.Origin) {
	for rel, info := range e.scanEntry(path, r) {
		if fs := e.st.Files[rel]; fs != nil && !fs.V.IsDeleted() {
			continue
		}
		e.newLocalVersion(rel, info, origin)
	}
}

// scanEntry returns the syncable files of an entry, keyed by path from ~,
// and refreshes the entry's skipped list.
func (e *Engine) scanEntry(path string, r proto.EntryRecord) map[string]fsx.Info {
	out := map[string]fsx.Info{}
	skipped := map[string]bool{}
	if r.Dir {
		res, err := fsx.Scan(e.abs(path), e.matchers[path], e.o.MaxFileSize)
		if err != nil {
			e.log.Error("scan", "entry", path, "err", err)
		}
		for f, info := range res.Files {
			out[path+"/"+f] = info
		}
		for _, f := range res.Skipped {
			skipped[path+"/"+f] = true
		}
	} else {
		info, _ := fsx.Stat(e.abs(path))
		switch {
		case info.Kind == fsx.Regular && info.Size > e.o.MaxFileSize, info.Kind == fsx.Other:
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
		if !r.Removed {
			e.rescanEntry(path)
		}
	}
}

func (e *Engine) rescanEntry(path string) {
	r := e.st.Entries[path]
	if e.rootPresent(path, r) {
		delete(e.missing, path)
	}
	e.scanEntry(path, r)
	e.refreshUnder(path, path)
}

// onLocalPath handles one watcher-reported path.
func (e *Engine) onLocalPath(abs string) {
	rel, ok := e.rel(abs)
	if !ok {
		return
	}
	if entry, _, ok := e.entryFor(rel); ok {
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
func (e *Engine) refreshUnder(entry, rel string) {
	seen := map[string]bool{}
	if info, _ := fsx.Stat(e.abs(rel)); info.Kind == fsx.Dir {
		res, _ := fsx.Scan(e.abs(rel), fsx.NewMatcher(nil), 0)
		for f := range res.Files {
			p := path.Join(rel, f)
			seen[p] = true
			e.refreshFile(entry, p)
		}
	} else {
		seen[rel] = true
		e.refreshFile(entry, rel)
	}
	for p := range e.st.Files {
		if !seen[p] && strings.HasPrefix(p, rel+"/") {
			e.refreshFile(entry, p)
		}
	}
}

// refreshFile makes the current version of rel match the disk, creating a
// local version (edit or delete) if it does not.
func (e *Engine) refreshFile(entry, rel string) {
	if e.ignored(entry, rel) {
		return
	}
	info, err := fsx.Stat(e.abs(rel))
	if err != nil {
		e.log.Error("stat", "path", rel, "err", err)
		return
	}
	fs := e.st.Files[rel]
	sk := e.skipped[entry]
	if sk == nil {
		sk = map[string]bool{}
		e.skipped[entry] = sk
	}
	switch info.Kind {
	case fsx.Regular:
		if info.Size > e.o.MaxFileSize {
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
		e.newLocalVersion(rel, info, version.Edit)
	case fsx.Missing:
		delete(sk, rel)
		if fs == nil || fs.V.IsDeleted() || !fs.Materialized {
			return // never had it here: not a delete
		}
		if !e.rootPresent(entry, e.st.Entries[entry]) {
			e.markMissing(entry)
			return
		}
		v := version.Version{Hash: version.Deleted, Vector: e.bump(fs.V.Vector), Parents: []string{fs.V.Hash},
			Mtime: time.Now().UnixNano(), By: e.o.MachineID, ByName: e.o.Name}
		e.log.Debug("local delete", "path", rel, "v", fmtVersion(&v))
		e.event(EvDeleted, rel, "", "")
		e.setCurrent(rel, v, false)
		e.broadcast(&proto.Msg{Type: proto.Announce, Path: rel, Version: &v}, nil)
	case fsx.Other:
		sk[rel] = true
	}
}

// markMissing applies the vanished-root guard (§6.5) to entry.
func (e *Engine) markMissing(entry string) {
	if !e.missing[entry] {
		e.log.Warn("entry root vanished; not propagating deletes", "entry", entry)
		e.event(EvError, entry, "", "the folder vanished here; deletes are not sent until it is back")
	}
	e.missing[entry] = true
}

// rootVanished: the entry's root is gone although this machine had files
// under it. A brand-new entry's root is merely not created yet (D6).
func (e *Engine) rootVanished(entry string) bool {
	if e.rootPresent(entry, e.st.Entries[entry]) {
		return false
	}
	for rel, fs := range e.st.Files {
		if fs.Materialized && !fs.V.IsDeleted() && (rel == entry || strings.HasPrefix(rel, entry+"/")) {
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
		Exec: info.Exec, By: e.o.MachineID, ByName: e.o.Name, Origin: origin}
	e.log.Debug("local version", "path", rel, "v", fmtVersion(&v))
	if origin == version.Edit { // not every file of a new entry
		e.event(EvChanged, rel, "", "")
	}
	if data, err := os.ReadFile(e.abs(rel)); err == nil && fsx.Hash(data) == v.Hash {
		e.keep(data)
	}
	e.setCurrent(rel, v, true)
	e.broadcast(&proto.Msg{Type: proto.Announce, Path: rel, Version: &v}, nil)
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

// accept reports the entry covering rel if remote changes to it may apply.
func (e *Engine) accept(rel string) (string, bool) {
	entry, _, ok := e.entryFor(rel)
	if !ok || e.ignored(entry, rel) || e.missing[entry] {
		return "", false
	}
	return entry, true
}

func (e *Engine) onAnnounce(p *peer, rel string, in version.Version) {
	entry, ok := e.accept(rel)
	if !ok {
		return
	}
	e.refreshFile(entry, rel)
	d := Decide(rel, e.current(rel), in, false)
	if err := e.execute(p, entry, rel, in, nil, nil, d); err != nil && !errors.Is(err, fsx.ErrChanged) {
		e.log.Error("apply", "path", rel, "err", err)
		e.event(EvError, rel, p.name, err.Error())
	}
}

func (e *Engine) onFetch(p *peer, rel string) {
	entry, _, ok := e.entryFor(rel)
	if !ok {
		return
	}
	for attempt := 0; attempt < maxApplyAttempts; attempt++ {
		e.refreshFile(entry, rel)
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
	if !in.IsDeleted() && fsx.Hash(content) != in.Hash {
		e.log.Error("content does not match its hash; dropped", "path", rel, "peer", p.name)
		return
	}
	if !in.IsDeleted() {
		e.keep(content)
	}
	for attempt := 0; attempt < maxApplyAttempts; attempt++ {
		e.refreshFile(entry, rel)
		cur := e.current(rel)
		d := Decide(rel, cur, in, true)
		if m, ok := e.tryMerge(rel, cur, in, anc, content); ok {
			d = DecideMerged(rel, cur, in, m)
		}
		err := e.execute(p, entry, rel, in, anc, content, d)
		if !errors.Is(err, fsx.ErrChanged) {
			if err != nil {
				e.log.Error("apply", "path", rel, "err", err)
				e.event(EvError, rel, p.name, err.Error())
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
	if cur == nil || cur.IsDeleted() || in.IsDeleted() || cur.Hash == in.Hash ||
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
		e.broadcast(&proto.Msg{Type: proto.Announce, Path: rel, Version: &v}, skip)
	}
	abs := e.abs(rel)
	switch d.Action {
	case Ignore:
		return nil
	case Fetch:
		p.send(&proto.Msg{Type: proto.Fetch, Path: rel})
		return nil
	case Record:
		if d.Copy != nil && d.Copy.FromIncoming {
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
		basis, onDisk := "", false
		if fs := e.st.Files[rel]; fs != nil && fs.Materialized && !fs.V.IsDeleted() {
			basis, onDisk = fs.V.Hash, true
		}
		if d.Backup || (d.Copy != nil && !d.Copy.FromIncoming) {
			local, err := os.ReadFile(abs)
			if err != nil {
				return err
			}
			if !onDisk || fsx.Hash(local) != basis {
				return fsx.ErrChanged
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
		opts := fsx.WriteOpts{Mtime: d.Result.Mtime, Exec: d.Result.Exec, ExpectAbsent: !onDisk, ExpectHash: basis}
		if _, err := fsx.Write(abs, content, opts); err != nil {
			if errors.Is(err, fsx.ErrNotRegular) {
				e.log.Warn("not overwriting a non-regular file", "path", rel)
				return nil
			}
			return err
		}
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
	e.event(EvConflict, c.Path, "", orig)
	if entry, _, ok := e.entryFor(c.Path); ok && !e.ignored(entry, c.Path) {
		if cur := e.current(c.Path); cur == nil || version.Compare(c.Version.Vector, cur.Vector) == version.After {
			e.setCurrent(c.Path, c.Version, true)
			v := c.Version
			e.broadcast(&proto.Msg{Type: proto.Announce, Path: c.Path, Version: &v}, nil)
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
