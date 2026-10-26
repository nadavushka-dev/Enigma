package engine

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/nadavushka-dev/enigma/internal/proto"
	"github.com/nadavushka-dev/enigma/internal/version"
)

// Groups (SPEC §9): a group is a named set of entries and the machines that
// sync them. Records live in fleet state, last writer wins, like machines.

var groupName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// GroupInfo is a group as the API reports it: Members expanded (main is
// "all except"), with the members' names alongside.
type GroupInfo struct {
	proto.GroupRecord
	MemberNames []string `json:"member_names"`
	Entries     int      `json:"entries"` // live entries in it
}

// ensureMain creates main when it is missing: the first machine of a fleet,
// or a fleet upgraded from v0.2. Main is every machine in the fleet
// (All), so every machine creates the same record and the last writer
// winning changes nothing. Called from New before the loop starts.
func (e *Engine) ensureMain() {
	if g, ok := e.st.Groups[proto.MainGroup]; ok && !g.Removed {
		return
	}
	e.st.Lamport++
	e.applyGroup(proto.GroupRecord{ID: proto.MainGroup, Name: "main", All: true, Clock: e.st.Lamport, By: e.o.MachineID}, nil)
}

// members lists a group's machines (IDs, sorted); loop only.
func (e *Engine) members(g proto.GroupRecord) []string {
	if !g.All {
		return g.Members
	}
	var out []string
	for id, m := range e.st.Machines {
		if !m.Removed && g.Has(id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// inGroup reports whether machine syncs g's entries; loop only.
func (e *Engine) inGroup(g proto.GroupRecord, machine string) bool {
	if m, ok := e.st.Machines[machine]; !ok || m.Removed {
		return false
	}
	return g.Has(machine)
}

// applyGroup merges a record (local when from is nil) into fleet state.
func (e *Engine) applyGroup(r proto.GroupRecord, from *peer) {
	old, had := e.st.Groups[r.ID]
	if had && !r.Newer(old) {
		return
	}
	if r.Clock > e.st.Lamport {
		e.st.Lamport = r.Clock
	}
	sort.Strings(r.Members)
	sort.Strings(r.Except)
	e.st.Groups[r.ID] = r
	e.dirty = true
	e.broadcast(&proto.Msg{Type: proto.Groups, Groups: []proto.GroupRecord{r}}, from)
	by := ""
	if from != nil {
		by = from.name
	}
	switch {
	case r.Removed && had && !old.Removed:
		e.event(EvGroup, "", by, "group "+old.Name+" deleted")
	case !r.Removed && (!had || old.Removed):
		e.event(EvGroup, "", by, "group "+r.Name+" created")
	case !r.Removed && had && old.Name != r.Name:
		e.event(EvGroup, "", by, "group "+old.Name+" renamed to "+r.Name)
	case !r.Removed && had && strings.Join(e.members(old), ",") != strings.Join(e.members(r), ","):
		e.event(EvGroup, "", by, "group "+r.Name+" members: "+e.memberNames(e.members(r)))
	}
	// Membership decides what is synced here and what each link carries:
	// watch or freeze this machine's entries, and tell every peer what it
	// shares with this machine now (new members get the entries).
	e.refreshScope()
	for _, p := range e.peers {
		e.sendScope(p)
	}
}

func (e *Engine) memberNames(ids []string) string {
	var names []string
	for _, id := range ids {
		if m, ok := e.st.Machines[id]; ok {
			names = append(names, m.Name)
		} else {
			names = append(names, id)
		}
	}
	return strings.Join(names, ", ")
}

// groupName is the display name of a group ID; loop only.
func (e *Engine) groupName(id string) string {
	if g, ok := e.st.Groups[id]; ok {
		return g.Name
	}
	return id
}

// live returns the group with that ID, if it exists and is not removed.
func (e *Engine) liveGroup(id string) (proto.GroupRecord, error) {
	g, ok := e.st.Groups[id]
	if !ok || g.Removed {
		return g, fmt.Errorf("no group %q (enigma group list shows them)", id)
	}
	return g, nil
}

// Groups returns the live groups, main first, then by name.
func (e *Engine) Groups() []GroupInfo {
	var out []GroupInfo
	e.do(func() {
		count := map[string]int{}
		for _, r := range e.st.Entries {
			if !r.Removed {
				count[r.Group]++
			}
		}
		for _, g := range e.st.Groups {
			if !g.Removed {
				g.Members = e.members(g) // expanded for All groups
				names := make([]string, 0, len(g.Members))
				for _, id := range g.Members {
					names = append(names, e.st.Machines[id].Name)
				}
				sort.Strings(names)
				out = append(out, GroupInfo{GroupRecord: g, MemberNames: names, Entries: count[g.ID]})
			}
		}
	})
	sort.Slice(out, func(i, j int) bool {
		if (out[i].ID == proto.MainGroup) != (out[j].ID == proto.MainGroup) {
			return out[i].ID == proto.MainGroup
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Entries returns the live entry records, by path.
func (e *Engine) Entries() []proto.EntryRecord {
	var out []proto.EntryRecord
	e.do(func() {
		for _, r := range e.st.Entries {
			if !r.Removed {
				out = append(out, r)
			}
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// CreateGroup makes a group with those members (machine IDs) and returns
// its ID.
func (e *Engine) CreateGroup(name string, members []string) (string, error) {
	if !groupName.MatchString(name) {
		return "", errors.New("a group name is letters, digits, dots, dashes or underscores")
	}
	if name == "main" {
		return "", errors.New("main is the group every fleet has")
	}
	id := newGroupID()
	var ferr error
	e.do(func() {
		for _, g := range e.st.Groups {
			if !g.Removed && g.Name == name {
				ferr = fmt.Errorf("a group named %s already exists", name)
				return
			}
		}
		if ferr = e.checkMembers(members); ferr != nil {
			return
		}
		e.st.Lamport++
		e.applyGroup(proto.GroupRecord{ID: id, Name: name, Members: append([]string(nil), members...), Clock: e.st.Lamport, By: e.o.MachineID}, nil)
	})
	return id, ferr
}

func (e *Engine) checkMembers(ids []string) error {
	for _, id := range ids {
		if m, ok := e.st.Machines[id]; !ok || m.Removed {
			return fmt.Errorf("no machine %q in the fleet", id)
		}
	}
	return nil
}

// DeleteGroup removes an empty group; main stays.
func (e *Engine) DeleteGroup(id string) error {
	if id == proto.MainGroup {
		return errors.New("main cannot be deleted")
	}
	var ferr error
	e.do(func() {
		g, err := e.liveGroup(id)
		if err != nil {
			ferr = err
			return
		}
		// Its entries stop syncing (as Remove does): the files stay where
		// they are on every machine (SPEC §9).
		for _, r := range e.st.Entries {
			if !r.Removed && r.Group == id {
				e.st.Lamport++
				r.Clock, r.By, r.Removed = e.st.Lamport, e.o.MachineID, true
				e.applyEntry(r, nil)
			}
		}
		e.st.Lamport++
		g.Clock, g.By, g.Removed = e.st.Lamport, e.o.MachineID, true
		e.applyGroup(g, nil)
	})
	return ferr
}

// RenameGroup gives a group a new name; main keeps its name.
func (e *Engine) RenameGroup(id, name string) error {
	if id == proto.MainGroup {
		return errors.New("main cannot be renamed")
	}
	if !groupName.MatchString(name) || name == "main" {
		return errors.New("a group name is letters, digits, dots, dashes or underscores, and not main")
	}
	var ferr error
	e.do(func() {
		g, err := e.liveGroup(id)
		if err != nil {
			ferr = err
			return
		}
		for _, o := range e.st.Groups {
			if !o.Removed && o.Name == name && o.ID != id {
				ferr = fmt.Errorf("a group named %s already exists", name)
				return
			}
		}
		e.st.Lamport++
		g.Clock, g.By, g.Name = e.st.Lamport, e.o.MachineID, name
		e.applyGroup(g, nil)
	})
	return ferr
}

// GroupMembers adds and removes machines (by ID) from a group.
func (e *Engine) GroupMembers(id string, add, remove []string) error {
	var ferr error
	e.do(func() {
		g, err := e.liveGroup(id)
		if err != nil {
			ferr = err
			return
		}
		if ferr = e.checkMembers(append(append([]string(nil), add...), remove...)); ferr != nil {
			return
		}
		// An All group keeps an exclusion list; the others a member list.
		have, want := g.Members, &g.Members
		if g.All {
			have, want = g.Except, &g.Except
			add, remove = remove, add
		}
		set := map[string]bool{}
		for _, m := range have {
			set[m] = true
		}
		for _, m := range add {
			set[m] = true
		}
		for _, m := range remove {
			delete(set, m)
		}
		var next []string
		for m := range set {
			next = append(next, m)
		}
		sort.Strings(next)
		if strings.Join(next, ",") == strings.Join(have, ",") {
			return
		}
		e.st.Lamport++
		g.Clock, g.By, *want = e.st.Lamport, e.o.MachineID, next
		e.applyGroup(g, nil)
	})
	return ferr
}

// Plural is "1 one" or "n many".
func Plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func newGroupID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ---- scope (SPEC §9) -------------------------------------------------------------

// shares reports whether entry r travels over the link to peer: both this
// machine and the peer are members of r's group. Loop only.
func (e *Engine) shares(r proto.EntryRecord, peer string) bool {
	g, ok := e.st.Groups[r.Group]
	if !ok || g.Removed {
		return false
	}
	return e.inGroup(g, e.o.MachineID) && e.inGroup(g, peer)
}

// sharesPath is shares for the entry covering rel; false when none does.
func (e *Engine) sharesPath(rel string, peer string) bool {
	_, r, ok := e.entryFor(rel)
	return ok && e.shares(r, peer)
}

// announce sends a new current version of rel to every peer in scope
// except skip (may be nil).
func (e *Engine) announce(rel string, v version.Version, skip *peer) {
	m := &proto.Msg{Type: proto.Announce, Path: rel, Version: &v}
	for _, p := range e.peers {
		if p != skip && e.sharesPath(rel, p.id) {
			p.send(m)
		}
	}
}

// sendScope sends p the entry records it shares with this machine and a
// summary of their files (SPEC §6.5): on link-up, and again whenever a
// group changes, since p may now be in scope for more. Both are
// idempotent on the receiving side.
func (e *Engine) sendScope(p *peer) {
	recs := make([]proto.EntryRecord, 0, len(e.st.Entries))
	inScope := map[string]bool{}
	for path, r := range e.st.Entries {
		if e.shares(r, p.id) {
			recs = append(recs, r)
			inScope[path] = true
		}
	}
	p.send(&proto.Msg{Type: proto.Entries, Entries: recs})
	chunk := map[string]version.Version{}
	flush := func() {
		if len(chunk) > 0 {
			p.send(&proto.Msg{Type: proto.Summary, Summary: chunk})
			chunk = map[string]version.Version{}
		}
	}
	for path, fs := range e.st.Files {
		if entry, _, ok := e.entryFor(path); !ok || !inScope[entry] {
			continue
		}
		chunk[path] = fs.V
		if len(chunk) >= summaryChunk {
			flush()
		}
	}
	flush()
	p.send(&proto.Msg{Type: proto.SummaryDone})
}

// sendEntrySummary sends p the current versions under one entry.
func (e *Engine) sendEntrySummary(p *peer, entry string) {
	chunk := map[string]version.Version{}
	for path, fs := range e.st.Files {
		if path == entry || strings.HasPrefix(path, entry+"/") {
			chunk[path] = fs.V
		}
	}
	if len(chunk) > 0 {
		p.send(&proto.Msg{Type: proto.Summary, Summary: chunk})
	}
}
