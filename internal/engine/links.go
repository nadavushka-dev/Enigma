package engine

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/trash"
	"github.com/nadavushka-dev/enigma/internal/version"
)

// Link replication (SPEC §8.1, D23). A symlink inside a folder entry with
// links on is a tracked path whose content is its stored target. It is
// never followed: only the link itself is read, written and trashed.

// Link states, in EntryStatus.Links.
const (
	LinkSynced   = "synced"   // replicated; its target is here
	LinkDangling = "dangling" // replicated, but its target is not here (left alone)
	LinkPending  = "pending"  // received; waiting for its target before it is created
	LinkSkipped  = "skipped"  // not replicated (Reason says why)
	LinkBlocked  = "blocked"  // received, but a folder or other non-file is in the way
)

// PendingWarnAfter is how long a link may wait for its target before it is
// a problem (SPEC §13.5).
const PendingWarnAfter = time.Minute

// LinkStatus is one link of an entry, for status, doctor and the TUI.
type LinkStatus struct {
	Path   string    `json:"path"`             // relative to ~
	Target string    `json:"target,omitempty"` // stored form: ~/… or relative to the link
	State  string    `json:"state"`
	Reason string    `json:"reason,omitempty"`
	Since  time.Time `json:"since,omitempty"` // pending, skipped and blocked: since when
}

// linkNote is a link that is skipped, pending or blocked here; loop only.
type linkNote struct {
	entry, target, state, reason string
	since                        time.Time
}

// pendingLink is a received link held until its target exists.
type pendingLink struct {
	p       *peer
	in      version.Version
	anc     []version.Node
	content []byte
	target  string // relative to ~
}

var errLinkOutside = errors.New("target outside your home folder")

// storeLinkTarget turns raw, the target of the link at linkRel as read on
// this machine, into its stored form, and resolves it (by path arithmetic,
// never touching the disk) to a path from ~. homes are this machine's home
// folder as resolved and as given. An absolute target under ~ is stored as
// ~/rel, so each machine writes it under its own home; a relative one is
// kept verbatim (a literal "~" first component gets a "./" so it never
// reads as home). Anything else is refused.
func storeLinkTarget(homes []string, linkRel, raw string) (stored, target string, err error) {
	if raw == "" || strings.ContainsRune(raw, 0) {
		return "", "", errors.New("empty or invalid target")
	}
	if filepath.IsAbs(raw) {
		p := filepath.Clean(raw)
		for _, h := range homes {
			r, err := filepath.Rel(h, p)
			if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
				continue
			}
			if r == "." {
				return "~", ".", nil
			}
			r = filepath.ToSlash(r)
			return "~/" + r, r, nil
		}
		return "", "", errLinkOutside
	}
	stored = raw
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		stored = "./" + raw
	}
	target, ok := resolveRelative(linkRel, filepath.ToSlash(raw))
	if !ok {
		return "", "", errLinkOutside
	}
	return stored, target, nil
}

func resolveRelative(linkRel, rel string) (string, bool) {
	t := path.Clean(path.Join(path.Dir(linkRel), rel))
	if t == ".." || strings.HasPrefix(t, "../") || path.IsAbs(t) {
		return "", false
	}
	return t, true
}

// resolveStoredTarget is where a stored target (from a peer) leads, as a
// path from ~. Only forms storeLinkTarget makes are accepted.
func resolveStoredTarget(linkRel, stored string) (string, bool) {
	switch {
	case stored == "" || strings.ContainsRune(stored, 0) || path.IsAbs(stored):
		return "", false
	case stored == "~":
		return ".", true
	case strings.HasPrefix(stored, "~/"):
		t := stored[2:]
		if t == "" || path.Clean(t) != t || t == ".." || strings.HasPrefix(t, "../") {
			return "", false
		}
		return t, true
	}
	return resolveRelative(linkRel, stored)
}

// materializeLinkTarget is the raw target to write for stored on a
// machine whose home is home.
func materializeLinkTarget(home, stored string) string {
	switch {
	case stored == "~":
		return home
	case strings.HasPrefix(stored, "~/"):
		return filepath.Join(home, filepath.FromSlash(stored[2:]))
	}
	return stored
}

func (e *Engine) homes() []string { return []string{e.o.Home, e.givenHome} }

// linksOn: entry is a folder entry with link replication on.
func (e *Engine) linksOn(entry string) bool {
	r, ok := e.st.Entries[entry]
	return ok && !r.Removed && r.Dir && r.Links
}

// linkReason says why the link at linkRel in entry, leading to target
// (from ~), must not be replicated; "" when it may. Both the sender and
// the receiver ask (D23): the target must be inside a live entry of the
// link's own group that this machine syncs, not ignored there and not
// behind another link.
func (e *Engine) linkReason(entry, linkRel, target string) string {
	r := e.st.Entries[entry]
	if !r.Dir || !r.Links || r.Removed {
		return "link replication is off for this folder"
	}
	if target == linkRel || strings.HasPrefix(target, linkRel+"/") {
		return "it points at itself"
	}
	if target == "." || strings.HasPrefix(linkRel, target+"/") {
		// A harness following it would walk into it again and again.
		return "it points at a folder that contains it (a loop)"
	}
	for p, tr := range e.st.Entries {
		if tr.Removed || !(target == p || tr.Dir && strings.HasPrefix(target, p+"/")) {
			continue
		}
		switch {
		case tr.Group != r.Group || !e.inScope(tr):
			return "target is in another group"
		case e.ignored(p, target):
			return "target is ignored"
		case e.shadowed(p, target):
			return "target is behind another link"
		}
		return ""
	}
	return "target is not synced (not inside a synced entry)"
}

// linkInfo is the tracked form of the link at rel (info from fsx.Stat):
// its version hash and mtime, with its stored target. reason is set
// when it must not be replicated.
func (e *Engine) linkInfo(entry, rel string, info fsx.Info) (li fsx.Info, stored, reason string) {
	stored, target, err := storeLinkTarget(e.homes(), rel, info.Target)
	if err != nil {
		return fsx.Info{}, "", err.Error()
	}
	if reason := e.linkReason(entry, rel, target); reason != "" {
		return fsx.Info{}, stored, reason
	}
	h := fsx.LinkHash(stored)
	if _, err := os.Lstat(e.abs(target)); err != nil {
		// Dangling here: never sent as a new link or retarget, so no
		// machine waits for a target that may never come. One already
		// synced stays as it is (reported dangling).
		if fs := e.st.Files[rel]; fs == nil || fs.V.IsDeleted() || fs.V.Hash != h {
			return fsx.Info{}, stored, "its target ~/" + target + " is not here (a dangling link is not replicated)"
		}
	}
	return fsx.Info{Kind: fsx.Other, Link: true, Target: stored, Hash: h, Mtime: info.Mtime}, stored, ""
}

// noteLink records that the link at rel is skipped, pending or blocked.
func (e *Engine) noteLink(entry, rel, target, state, reason string) {
	since := time.Now()
	if old := e.linkNotes[rel]; old != nil && old.state == state && old.target == target {
		since = old.since
	}
	e.linkNotes[rel] = &linkNote{entry: entry, target: target, state: state, reason: reason, since: since}
}

// clearLinkNotes forgets the notes and pending links at or under rel.
func (e *Engine) clearLinkNotes(rel string, keepPending bool) {
	for p, n := range e.linkNotes {
		if (p == rel || strings.HasPrefix(p, rel+"/")) && !(keepPending && n.state == LinkPending) {
			delete(e.linkNotes, p)
		}
	}
	if keepPending {
		return
	}
	for p := range e.pending {
		if p == rel || strings.HasPrefix(p, rel+"/") {
			delete(e.pending, p)
		}
	}
}

// refreshLink is refreshFile for a symlink in an entry with links on.
func (e *Engine) refreshLink(entry, rel string, info fsx.Info, room *int) {
	li, stored, reason := e.linkInfo(entry, rel, info)
	if reason != "" {
		e.markSkipped(entry, rel)
		e.noteLink(entry, rel, stored, LinkSkipped, reason)
		return // not a delete: the link is still there
	}
	delete(e.skipped[entry], rel)
	if n := e.linkNotes[rel]; n != nil && n.state != LinkPending {
		delete(e.linkNotes, rel)
	}
	fs := e.st.Files[rel]
	if fs != nil && !fs.V.IsDeleted() && fs.V.Hash == li.Hash {
		if !fs.Materialized {
			fs.Materialized, e.dirty = true, true
		}
		return
	}
	if room != nil && (fs == nil || fs.V.IsDeleted()) {
		if *room <= 0 {
			e.markOverLimit(entry, rel)
			return
		}
		*room--
	}
	e.newLocalVersion(rel, li, version.Edit)
}

// linkOnDisk is the raw target of the link at rel, provided it still is
// the version with hash want; otherwise ErrChanged.
func (e *Engine) linkOnDisk(rel, want string) (string, error) {
	info, err := fsx.Stat(e.abs(rel))
	if err != nil {
		return "", err
	}
	if !info.Link {
		return "", fsx.ErrChanged
	}
	stored, _, err := storeLinkTarget(e.homes(), rel, info.Target)
	if err != nil || fsx.LinkHash(stored) != want {
		return "", fsx.ErrChanged
	}
	return info.Target, nil
}

// trashLink moves the link at rel (raw target raw) to the trash. It is no
// longer on disk here, so its absence is not a delete.
func (e *Engine) trashLink(rel, raw string) error {
	if _, err := trash.MoveLink(filepath.Join(e.o.DataDir, "trash"), e.abs(rel), rel, raw, time.Now()); err != nil {
		return err
	}
	if fs := e.st.Files[rel]; fs != nil {
		fs.Materialized, e.dirty = false, true
	}
	return nil
}

// retryPending applies the held links whose target now exists (D23).
// Run after every turn of the loop: an apply or a rescan may have
// created a target.
func (e *Engine) retryPending() {
	for rel, pl := range e.pending {
		if _, err := os.Lstat(e.abs(pl.target)); err != nil {
			continue
		}
		delete(e.pending, rel)
		if n := e.linkNotes[rel]; n != nil && n.state == LinkPending {
			delete(e.linkNotes, rel)
		}
		e.onFile(pl.p, rel, pl.in, pl.anc, pl.content)
	}
}

// SetLinks turns link replication on or off for the folder entry at abs,
// on every machine of its group (D23).
func (e *Engine) SetLinks(abs string, on bool) error {
	var err error
	if derr := e.do(func() {
		rel, ok := e.rel(abs)
		r, exists := e.st.Entries[rel]
		switch {
		case !ok || !exists || r.Removed:
			err = fmt.Errorf("%s is not synced", abs)
		case !r.Dir:
			err = fmt.Errorf("%s is a file; link replication is for synced folders", abs)
		case !e.inScope(r):
			err = fmt.Errorf("%s belongs to group %s, which this machine is not in", abs, e.groupName(r.Group))
		case r.Links == on:
		default:
			e.st.Lamport++
			r.Clock, r.By, r.Links = e.st.Lamport, e.o.MachineID, on
			e.applyEntry(r, nil)
		}
	}); derr != nil {
		return derr
	}
	return err
}

// relinkOthers rescans the entries with links on, but entry: a change to
// one entry (added, removed, moved) can make their links eligible or not.
func (e *Engine) relinkOthers(entry string) {
	for p, r := range e.st.Entries {
		if p != entry && !r.Removed && r.Dir && r.Links && e.inScope(r) {
			e.rescanEntry(p)
		}
	}
}

// pollLinks refreshes the links of every entry with links on. macOS file
// events (kqueue) follow a link and report nothing when one is removed or
// replaced by a rename (ln -sfn), so links are also polled (SPEC §6.1).
// Only folder listings are read, never a file.
func (e *Engine) pollLinks() {
	for entry, r := range e.st.Entries {
		if r.Removed || !r.Dir || !r.Links || !e.inScope(r) || e.missing[entry] {
			continue
		}
		seen := map[string]bool{}
		room := e.room(entry, r) // once per entry: refreshFile keeps it current
		for _, rel := range e.entryLinks(entry) {
			seen[rel] = true
			e.refreshFile(entry, rel, room)
		}
		for rel, fs := range e.st.Files {
			if !seen[rel] && fs.V.Link && !fs.V.IsDeleted() && strings.HasPrefix(rel, entry+"/") {
				e.refreshFile(entry, rel, room)
			}
		}
	}
}

// entryLinks lists the symlinks of a folder entry (paths from ~), without
// walking what the entry ignores.
func (e *Engine) entryLinks(entry string) []string {
	m := e.matchers[entry]
	if m == nil {
		m = fsx.NewMatcher(e.st.Entries[entry].Ignore)
	}
	links, _ := fsx.ListLinks(e.abs(entry), m)
	out := make([]string, len(links))
	for i, f := range links {
		out[i] = entry + "/" + f
	}
	return out
}

// linkStatuses lists the links of entry for status; loop only.
func (e *Engine) linkStatuses(entry string) []LinkStatus {
	var out []LinkStatus
	noted := map[string]bool{}
	under := func(rel string) bool { return strings.HasPrefix(rel, entry+"/") }
	for rel, n := range e.linkNotes {
		if n.entry == entry && under(rel) {
			noted[rel] = true
			out = append(out, LinkStatus{Path: rel, Target: n.target, State: n.state, Reason: n.reason, Since: n.since})
		}
	}
	for rel, fs := range e.st.Files {
		if noted[rel] || !under(rel) || !fs.V.Link || fs.V.IsDeleted() || !fs.Materialized {
			continue
		}
		info, err := fsx.Stat(e.abs(rel))
		if err != nil || !info.Link {
			continue
		}
		stored, target, err := storeLinkTarget(e.homes(), rel, info.Target)
		if err != nil {
			continue
		}
		ls := LinkStatus{Path: rel, Target: stored, State: LinkSynced}
		if _, err := os.Lstat(e.abs(target)); err != nil {
			ls.State, ls.Reason = LinkDangling, "its target ~/"+target+" is not here"
		}
		out = append(out, ls)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
