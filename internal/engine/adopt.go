package engine

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

// ProbeTimeout is how long Preview waits for connected members (SPEC §5).
// A slow or old peer is reported as unknown; the chosen policy still
// applies when it answers later or reconnects.
const ProbeTimeout = 3 * time.Second

// Adoption states of one path (D22).
const (
	AdoptionDifferent = "different"
	AdoptionOnlyHere  = "only-here"
	AdoptionOnlyThere = "only-there"
)

// AdoptionReport is what `enigma add` shows before creating an entry
// (SPEC §10, D22). Paths are relative to ~.
type AdoptionReport struct {
	Path      string          `json:"path"`
	Dir       bool            `json:"dir"`
	Group     string          `json:"group"`
	GroupName string          `json:"group_name"`
	Local     ProbeMachine    `json:"local"`
	Machines  []ProbeMachine  `json:"machines"` // every member of the group, local first
	Files     []AdoptionFile  `json:"files,omitempty"`
	Summary   AdoptionSummary `json:"summary"`
}

// ProbeMachine is one member's copy of the path.
type ProbeMachine struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Local          bool              `json:"local,omitempty"`
	Connected      bool              `json:"connected"`
	InGroup        bool              `json:"in_group"`
	Exists         bool              `json:"exists"`
	Dir            bool              `json:"dir,omitempty"`
	Files          []proto.ProbeFile `json:"files,omitempty"`
	Truncated      bool              `json:"truncated,omitempty"`
	Entry          bool              `json:"entry,omitempty"`            // already synced there
	EntryGroup     string            `json:"entry_group,omitempty"`      // in this group (ID)
	EntryGroupName string            `json:"entry_group_name,omitempty"` // ... and its display name
	Error          string            `json:"error,omitempty"`
}

// EntryGroupLabel is the group's display name, or its ID on an old reply.
func (m ProbeMachine) EntryGroupLabel() string {
	if m.EntryGroupName != "" {
		return m.EntryGroupName
	}
	if m.EntryGroup != "" {
		return m.EntryGroup
	}
	return proto.MainGroup
}

// AdoptionFile is one file that differs, or exists only on one side.
type AdoptionFile struct {
	Path     string   `json:"path"`               // relative to the entry root; "" for a single file
	State    string   `json:"state"`              // different, only-here, only-there
	Machines []string `json:"machines,omitempty"` // peers that differ or have it
}

// AdoptionSummary counts the files in an AdoptionReport.
type AdoptionSummary struct {
	Files     int `json:"files"`
	Same      int `json:"same"`
	Different int `json:"different"`
	OnlyHere  int `json:"only_here"`
	OnlyThere int `json:"only_there"`
	Unknown   int `json:"unknown"`   // group members that could not be asked
	Truncated int `json:"truncated"` // members with too many files to compare
}

// NeedsDecision reports whether the user must choose before adding.
func (r AdoptionReport) NeedsDecision() bool {
	for _, f := range r.Files {
		if f.State == AdoptionDifferent {
			return true
		}
	}
	return false
}

// Blocked reports a member whose answer makes the add unsafe: a file on
// one side and a folder on the other, a symlink, an unreadable path. An
// offline member is not blocking: the chosen policy applies later.
func (r AdoptionReport) Blocked() string {
	for _, m := range r.Machines {
		if m.Local || m.Error == "" || m.Error == "not connected" || m.Error == "no reply" {
			continue
		}
		return m.Name + ": " + m.Error
	}
	return ""
}

// SyncedElsewhere reports a member that already has this path as an entry.
// The local fleet state may simply be behind; adding anyway would create a
// second entry for one path, so callers refuse and point at the entry.
func (r AdoptionReport) SyncedElsewhere() (name, group string, ok bool) {
	for _, m := range r.Machines {
		if !m.Local && m.Entry {
			return m.Name, m.EntryGroupLabel(), true
		}
	}
	return "", "", false
}

type probeTarget struct {
	id, name  string
	connected bool
}

type previewSnapshot struct {
	abs       string
	rel       string
	group     string
	groupName string
	limits    Limits
	targets   []probeTarget
}

type probeManifest struct {
	Exists    bool
	Dir       bool
	Files     []proto.ProbeFile
	Truncated bool
	Error     string
}

type probeCollector struct {
	id, path, group string
	want            map[string]bool
	got             map[string]proto.ProbeReplyBody
	n               atomic.Int32
	ch              chan struct{}
}

// Preview asks the members of group what they have at abs and compares it
// with this machine (SPEC §5, §10, D22). It never changes state.
func (e *Engine) Preview(abs string, ignore []string, group string) (AdoptionReport, error) {
	var snap previewSnapshot
	var err error
	if derr := e.do(func() { snap, err = e.previewSnapshot(abs, group) }); derr != nil {
		return AdoptionReport{}, derr
	}
	if err != nil {
		return AdoptionReport{}, err
	}

	local := scanProbe(snap.abs, ignore, snap.limits)
	report := AdoptionReport{Path: snap.rel, Group: snap.group, GroupName: snap.groupName}
	report.Local = ProbeMachine{ID: e.o.MachineID, Name: e.o.Name, Local: true, Connected: true, InGroup: true,
		Exists: local.Exists, Dir: local.Dir, Files: local.Files, Truncated: local.Truncated, Error: local.Error}

	c := &probeCollector{id: fmt.Sprintf("%s-%d", e.o.MachineID, e.probeSeq.Add(1)),
		path: snap.rel, group: snap.group, want: map[string]bool{},
		got: map[string]proto.ProbeReplyBody{}, ch: make(chan struct{}, 1)}
	for _, t := range snap.targets {
		if t.connected {
			c.want[t.id] = true
		}
	}
	if len(c.want) > 0 {
		e.probeMu.Lock()
		e.probes[c.id] = c
		e.probeMu.Unlock()
		body := &proto.ProbeBody{ID: c.id, Path: snap.rel, Group: snap.group, Ignore: append([]string(nil), ignore...)}
		e.post(func() {
			for _, p := range e.peers {
				if c.want[p.id] {
					p.send(&proto.Msg{Type: proto.Probe, Probe: body})
				}
			}
		})
		timer := time.NewTimer(ProbeTimeout)
	wait:
		for c.n.Load() < int32(len(c.want)) {
			select {
			case <-c.ch:
			case <-timer.C:
				break wait
			case <-e.quit:
				timer.Stop()
				e.probeMu.Lock()
				delete(e.probes, c.id)
				e.probeMu.Unlock()
				return report, ErrClosed
			}
		}
		timer.Stop()
	}

	e.probeMu.Lock()
	got := make(map[string]proto.ProbeReplyBody, len(c.got))
	for id, b := range c.got {
		got[id] = b
	}
	delete(e.probes, c.id)
	e.probeMu.Unlock()

	e.fillAdoption(&report, local, snap.targets, got)
	return report, nil
}

func (e *Engine) previewSnapshot(abs, group string) (previewSnapshot, error) {
	rel, ok := e.rel(abs)
	if !ok {
		return previewSnapshot{}, fmt.Errorf("%s: only paths inside your home folder can be synced", abs)
	}
	if group == "" {
		group = proto.MainGroup
	}
	g, err := e.liveGroup(group)
	if err != nil {
		return previewSnapshot{}, err
	}
	if !e.inGroup(g, e.o.MachineID) {
		return previewSnapshot{}, fmt.Errorf("this machine is not a member of group %s; join it first (enigma group add %s %s)", g.Name, g.Name, e.o.Name)
	}
	for p, r := range e.st.Entries {
		if r.Removed {
			continue
		}
		switch {
		case p == rel:
			return previewSnapshot{}, fmt.Errorf("%s is already synced in %s; use enigma entry move", abs, e.groupName(r.Group))
		case r.Dir && strings.HasPrefix(rel, p+"/"):
			return previewSnapshot{}, fmt.Errorf("%s is inside the synced folder ~/%s", abs, p)
		case strings.HasPrefix(p, rel+"/"):
			return previewSnapshot{}, fmt.Errorf("%s contains the synced entry ~/%s; remove that first", abs, p)
		}
	}
	snap := previewSnapshot{abs: abs, rel: rel, group: group, groupName: g.Name, limits: e.lim()}
	for _, id := range e.members(g) {
		if id == e.o.MachineID {
			continue
		}
		rec := e.st.Machines[id]
		if rec.Removed {
			continue
		}
		name := rec.Name
		if name == "" {
			name = id
		}
		_, connected := e.peers[id]
		snap.targets = append(snap.targets, probeTarget{id: id, name: name, connected: connected})
	}
	sort.Slice(snap.targets, func(i, j int) bool { return snap.targets[i].name < snap.targets[j].name })
	return snap, nil
}

// scanProbe reads the metadata of abs, relative paths for a folder and ""
// for a single file. It never changes state.
func scanProbe(abs string, ignore []string, lim Limits) probeManifest {
	if st, ok := fsx.StampOf(abs); ok && lim.MaxFileSize > 0 && st.Size > lim.MaxFileSize {
		return probeManifest{Exists: true, Error: fmt.Sprintf("over the %s file limit", FormatSize(lim.MaxFileSize))}
	}
	info, err := fsx.Stat(abs)
	if err != nil {
		return probeManifest{Error: err.Error()}
	}
	switch info.Kind {
	case fsx.Missing:
		return probeManifest{}
	case fsx.Other:
		return probeManifest{Exists: true, Error: "not a regular file or folder"}
	case fsx.Dir:
		m := fsx.NewMatcher(ignore)
		n, err := fsx.CountFiles(abs, m, lim.MaxFileSize, lim.MaxFiles)
		if err != nil {
			return probeManifest{Exists: true, Dir: true, Error: err.Error()}
		}
		if n > lim.MaxFiles {
			return probeManifest{Exists: true, Dir: true, Truncated: true}
		}
		res, err := fsx.Scan(abs, m, lim.MaxFileSize)
		if err != nil {
			return probeManifest{Exists: true, Dir: true, Error: err.Error()}
		}
		files := make([]proto.ProbeFile, 0, len(res.Files))
		for rel, i := range res.Files {
			files = append(files, proto.ProbeFile{Path: rel, Hash: i.Hash, Size: i.Size, Mtime: i.Mtime, Exec: i.Exec})
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		return probeManifest{Exists: true, Dir: true, Files: files}
	case fsx.Regular:
		if lim.MaxFileSize > 0 && info.Size > lim.MaxFileSize {
			return probeManifest{Exists: true, Error: fmt.Sprintf("over the %s file limit", FormatSize(lim.MaxFileSize))}
		}
		return probeManifest{Exists: true, Files: []proto.ProbeFile{{Path: "", Hash: info.Hash, Size: info.Size, Mtime: info.Mtime, Exec: info.Exec}}}
	}
	return probeManifest{Exists: true, Error: "not a regular file or folder"}
}

// onProbe answers a read-only first-add probe (SPEC §5, D22). Both ends
// must be members of the group, so a non-member never learns the path.
func (e *Engine) onProbe(p *peer, b *proto.ProbeBody) {
	if b == nil || b.ID == "" {
		return
	}
	g, ok := e.st.Groups[b.Group]
	if !ok || g.Removed || !e.inGroup(g, p.id) || !e.inGroup(g, e.o.MachineID) {
		return
	}
	abs, ok := e.probePath(b.Path)
	if !ok {
		return
	}
	entry, _, hasEntry := e.entryFor(b.Path)
	entryGroup, entryGroupName := "", ""
	if hasEntry {
		entryGroup = e.st.Entries[entry].Group
		entryGroupName = e.groupName(entryGroup)
	}
	lim := e.lim()
	ignore := append([]string(nil), b.Ignore...)
	if len(ignore) > 100 {
		ignore = ignore[:100]
	}
	go func() {
		select {
		case e.probeSem <- struct{}{}:
		case <-e.quit:
			return
		}
		defer func() { <-e.probeSem }()
		m := scanProbe(abs, ignore, lim)
		body := &proto.ProbeReplyBody{ID: b.ID, Path: b.Path, Group: b.Group,
			Exists: m.Exists, Dir: m.Dir, Files: m.Files, Truncated: m.Truncated, Error: m.Error,
			Entry: hasEntry, EntryGroup: entryGroup, EntryGroupName: entryGroupName}
		e.post(func() {
			if e.peers[p.id] != p {
				return
			}
			p.send(&proto.Msg{Type: proto.ProbeReply, ProbeReply: body})
		})
	}()
}

func (e *Engine) probePath(rel string) (string, bool) {
	if rel == "" || path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", false
		}
	}
	abs := filepath.Join(e.o.Home, filepath.FromSlash(rel))
	if abs == e.o.Home || !strings.HasPrefix(abs, e.o.Home+string(os.PathSeparator)) {
		return "", false
	}
	return abs, true
}

// onProbeReply routes one answer to the waiting Preview. It runs on the
// engine loop but uses probeMu, since Preview waits off the loop.
func (e *Engine) onProbeReply(p *peer, b *proto.ProbeReplyBody) {
	if b == nil {
		return
	}
	e.probeMu.Lock()
	c := e.probes[b.ID]
	if c != nil && c.want[p.id] && b.Path == c.path && b.Group == c.group {
		if _, dup := c.got[p.id]; !dup {
			c.got[p.id] = *b
			c.n.Add(1)
			select {
			case c.ch <- struct{}{}:
			default:
			}
		}
	}
	e.probeMu.Unlock()
}

// fillAdoption turns the local manifest and the replies into the report.
func (e *Engine) fillAdoption(r *AdoptionReport, local probeManifest, targets []probeTarget, got map[string]proto.ProbeReplyBody) {
	r.Machines = append(r.Machines, r.Local)
	for _, t := range targets {
		pm := ProbeMachine{ID: t.id, Name: t.name, Connected: t.connected, InGroup: true}
		b, replied := got[t.id]
		switch {
		case replied:
			pm.Connected = true
			pm.Exists, pm.Dir, pm.Files = b.Exists, b.Dir, b.Files
			pm.Truncated, pm.Error, pm.Entry, pm.EntryGroup, pm.EntryGroupName = b.Truncated, b.Error, b.Entry, b.EntryGroup, b.EntryGroupName
		case !t.connected:
			pm.Error = "not connected"
		default:
			pm.Error = "no reply"
		}
		if local.Exists && pm.Exists && pm.Error == "" && local.Dir != pm.Dir {
			pm.Error = "file vs folder"
		}
		r.Machines = append(r.Machines, pm)
	}
	if local.Exists {
		r.Dir = local.Dir
	} else {
		for _, m := range r.Machines[1:] {
			if m.Exists && m.Error == "" {
				r.Dir = m.Dir
				break
			}
		}
	}
	r.Files, r.Summary = compareAdoption(r.Local, r.Machines[1:])
	// compareAdoption resets the summary; keep the per-machine counts.
	for _, m := range r.Machines[1:] {
		if m.Error != "" {
			r.Summary.Unknown++
		}
		if m.Truncated {
			r.Summary.Truncated++
		}
	}
}

func compareAdoption(local ProbeMachine, peers []ProbeMachine) ([]AdoptionFile, AdoptionSummary) {
	localFiles := map[string]proto.ProbeFile{}
	for _, f := range local.Files {
		localFiles[f.Path] = f
	}
	type namedFile struct {
		name string
		f    proto.ProbeFile
	}
	peerFiles := map[string][]namedFile{}
	keys := map[string]bool{}
	for _, m := range peers {
		if !m.Exists || m.Error != "" || m.Truncated {
			continue
		}
		for _, f := range m.Files {
			peerFiles[f.Path] = append(peerFiles[f.Path], namedFile{name: m.Name, f: f})
			keys[f.Path] = true
		}
	}
	for k := range localFiles {
		keys[k] = true
	}
	paths := make([]string, 0, len(keys))
	for k := range keys {
		paths = append(paths, k)
	}
	sort.Strings(paths)

	var out []AdoptionFile
	var sum AdoptionSummary
	sum.Files = len(paths)
	for _, k := range paths {
		lf, hasLocal := localFiles[k]
		pf := peerFiles[k]
		switch {
		case hasLocal && len(pf) == 0:
			out = append(out, AdoptionFile{Path: k, State: AdoptionOnlyHere})
			sum.OnlyHere++
		case !hasLocal && len(pf) > 0:
			names := make([]string, 0, len(pf))
			for _, p := range pf {
				names = append(names, p.name)
			}
			out = append(out, AdoptionFile{Path: k, State: AdoptionOnlyThere, Machines: uniqueStrings(names)})
			sum.OnlyThere++
		case hasLocal && len(pf) > 0:
			var diff []string
			for _, p := range pf {
				if p.f.Hash != lf.Hash || p.f.Exec != lf.Exec {
					diff = append(diff, p.name)
				}
			}
			if len(diff) > 0 {
				out = append(out, AdoptionFile{Path: k, State: AdoptionDifferent, Machines: uniqueStrings(diff)})
				sum.Different++
			} else {
				sum.Same++
			}
		}
	}
	return out, sum
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
