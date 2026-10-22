package engine

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nadavushka-dev/enigma/internal/buildinfo"
	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/history"
	"github.com/nadavushka-dev/enigma/internal/proto"
	"github.com/nadavushka-dev/enigma/internal/trash"
	"github.com/nadavushka-dev/enigma/internal/version"
	"github.com/nadavushka-dev/enigma/internal/watch"
)

// Defaults from SPEC §8 (D18) and §6.1.
const (
	DefaultMaxFileSize    = 10 << 20
	DefaultMaxFiles       = 2000
	DefaultDebounce       = 200 * time.Millisecond
	DefaultRescanInterval = 10 * time.Minute
	// DefaultHeartbeat: each side pings every interval and drops a link
	// silent for three. ssh keepalive only protects the dialing side; the
	// accepting side would otherwise keep a dead link for many minutes.
	DefaultHeartbeat = 15 * time.Second
)

var ErrClosed = errors.New("engine closed")

type Options struct {
	Home      string // the user's home directory; entries live under it
	DataDir   string // state, trash, backups (SPEC §10)
	MachineID string
	Name      string // display name
	GroupID   string

	MaxFileSize    int64
	MaxFiles       int
	Debounce       time.Duration
	RescanInterval time.Duration
	Heartbeat      time.Duration
	NoWatch        bool // no fs watcher: callers drive changes with Rescan (tests, simulation)
	Log            *slog.Logger
	// Notify raises a desktop notification about path (SPEC §12.3); nil
	// is silent. It is called on the loop, so it must return at once.
	Notify func(path, title, body string)

	// Machines (SPEC §11).
	Bin        string                      // this machine's enigma binary, for its own record
	Bootstrap  []proto.MachineRecord       // records handed over by `join`
	OnMachines func([]proto.MachineRecord) // called (off the loop) after the records change

	// One version everywhere (SPEC §11).
	Version       string                                 // default: buildinfo.Version
	Platform      string                                 // default: buildinfo.Platform()
	BinaryFor     func(platform string) ([]byte, error)  // a binary of this version for platform
	InstallBinary func(version string, bin []byte) error // replace this binary and restart
}

// Engine is one machine's sync engine. All state is owned by a single loop
// goroutine; every input (fs events, peer messages, API calls) is a closure
// run on that loop, so decisions never race.
type Engine struct {
	o   Options
	log *slog.Logger
	// givenHome is Home as given, before symlinks were resolved: paths
	// from the CLI and TUI are under it (macOS /tmp, network homes).
	givenHome string
	w         *watch.Watcher

	inbox chan func()
	quit  chan struct{}
	done  chan struct{}
	once  sync.Once

	sent, received atomic.Int64

	// Loop-owned.
	st        *state
	hist      *history.Store
	statePath string
	peers     map[string]*peer
	matchers  map[string]*fsx.Matcher
	skipped   map[string]map[string]bool // entry -> rel -> skipped
	missing   map[string]bool            // entry root vanished locally (§6.5 guard)
	dirty     bool

	machinesChanged chan struct{} // coalesced OnMachines notifications

	// hashStat reads and hashes a file (fsx.Stat; tests count calls), and
	// settle is how old a stamp must be to be trusted (see statFile).
	hashStat func(string) (fsx.Info, error)
	settle   time.Duration
	events   []Event // recent activity (SPEC §12.2)
	evSeq    uint64
}

func New(o Options) (*Engine, error) {
	if o.Home == "" || o.DataDir == "" || o.MachineID == "" || o.GroupID == "" {
		return nil, errors.New("engine: Home, DataDir, MachineID and GroupID are required")
	}
	givenHome := filepath.Clean(o.Home)
	home, err := filepath.EvalSymlinks(o.Home)
	if err != nil {
		return nil, fmt.Errorf("home: %w", err)
	}
	o.Home = home
	if o.Name == "" {
		o.Name = o.MachineID
	}
	if o.MaxFileSize == 0 {
		o.MaxFileSize = DefaultMaxFileSize
	}
	if o.MaxFiles == 0 {
		o.MaxFiles = DefaultMaxFiles
	}
	if o.Debounce == 0 {
		o.Debounce = DefaultDebounce
	}
	if o.RescanInterval == 0 {
		o.RescanInterval = DefaultRescanInterval
	}
	if o.Heartbeat == 0 {
		o.Heartbeat = DefaultHeartbeat
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Version == "" {
		o.Version = buildinfo.Version
	}
	if o.Platform == "" {
		o.Platform = buildinfo.Platform()
	}
	e := &Engine{
		o: o, log: o.Log.With("machine", o.Name), givenHome: givenHome,
		inbox: make(chan func(), 256), quit: make(chan struct{}), done: make(chan struct{}),
		statePath: filepath.Join(o.DataDir, "state.json"),
		hist:      history.Open(filepath.Join(o.DataDir, "history")),
		peers:     map[string]*peer{}, matchers: map[string]*fsx.Matcher{},
		skipped: map[string]map[string]bool{}, missing: map[string]bool{},
		machinesChanged: make(chan struct{}, 1),
		hashStat:        fsx.Stat, settle: stampSettle,
	}
	if e.st, err = loadState(e.statePath); err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	if !o.NoWatch {
		if e.w, err = watch.New(o.Debounce); err != nil {
			return nil, err
		}
	}
	for path, r := range e.st.Entries {
		if !r.Removed {
			e.track(path, r)
		}
	}
	for _, r := range o.Bootstrap {
		e.applyMachine(r, nil)
	}
	if _, ok := e.st.Machines[o.MachineID]; !ok {
		// The first machine: nobody added it, so it has no target (§11).
		e.st.Lamport++
		e.applyMachine(proto.MachineRecord{ID: o.MachineID, Name: o.Name, Bin: o.Bin, Clock: e.st.Lamport, By: o.MachineID}, nil)
	}
	if o.OnMachines != nil {
		go e.notifyMachines()
	}
	e.rescanAll() // changes made while the daemon was not running (§6.5)
	e.saveIfDirty()
	e.prune()
	go e.loop()
	return e, nil
}

func (e *Engine) loop() {
	defer close(e.done)
	var events <-chan watch.Batch
	if e.w != nil {
		events = e.w.Events()
	}
	tick := time.NewTicker(e.o.RescanInterval)
	defer tick.Stop()
	for {
		select {
		case <-e.quit:
			return
		case f := <-e.inbox:
			f()
		case b, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if b.Rescan {
				e.rescanAll()
			} else {
				for _, p := range b.Paths {
					e.onLocalPath(p)
				}
			}
		case <-tick.C:
			e.rescanAll()
			e.prune()
		}
		e.saveIfDirty()
	}
}

func (e *Engine) saveIfDirty() {
	if !e.dirty {
		return
	}
	if err := e.st.save(e.statePath); err != nil {
		e.log.Error("save state", "err", err)
		return
	}
	e.dirty = false
}

// post queues f on the loop without waiting.
func (e *Engine) post(f func()) {
	select {
	case e.inbox <- f:
	case <-e.quit:
	}
}

// do runs f on the loop and waits for it.
func (e *Engine) do(f func()) error {
	ran := make(chan struct{})
	select {
	case e.inbox <- func() { f(); close(ran) }:
	case <-e.quit:
		return ErrClosed
	}
	select {
	case <-ran:
		return nil
	case <-e.done:
		return ErrClosed
	}
}

func (e *Engine) Close() error {
	e.once.Do(func() {
		close(e.quit)
		<-e.done
		for _, p := range e.peers {
			p.close()
		}
		if e.w != nil {
			e.w.Close()
		}
		e.saveIfDirty()
	})
	return nil
}

// ---- public API -------------------------------------------------------------

// Add starts syncing the file or folder at abs (D6, D7).
func (e *Engine) Add(abs string, ignore []string) error {
	var err error
	if derr := e.do(func() { err = e.addEntry(abs, ignore) }); derr != nil {
		return derr
	}
	return err
}

// Remove stops syncing the entry at abs on every machine; files stay (§9).
func (e *Engine) Remove(abs string) error {
	var err error
	if derr := e.do(func() {
		rel, ok := e.rel(abs)
		r, exists := e.st.Entries[rel]
		if !ok || !exists || r.Removed {
			err = fmt.Errorf("%s is not synced", abs)
			return
		}
		e.st.Lamport++
		r.Clock, r.By, r.Removed = e.st.Lamport, e.o.MachineID, true
		e.applyEntry(r, nil)
	}); derr != nil {
		return derr
	}
	return err
}

// Rescan compares every entry against disk now.
func (e *Engine) Rescan() { e.do(e.rescanAll) }

type EntryStatus struct {
	Path    string
	Dir     bool
	Missing bool     // root vanished locally; deletes are not propagated
	Skipped []string // files not synced: symlinks, oversized
}

type PeerStatus struct{ ID, Name string }

type Status struct {
	Entries        []EntryStatus
	Peers          []PeerStatus
	Files          int      // tracked, not deleted
	Conflicts      []string // conflict copies on this machine's disk, from ~
	Sent, Received int64
}

func (e *Engine) Status() Status {
	var s Status
	e.do(func() {
		for path, r := range e.st.Entries {
			if r.Removed {
				continue
			}
			es := EntryStatus{Path: path, Dir: r.Dir, Missing: e.missing[path]}
			for rel := range e.skipped[path] {
				es.Skipped = append(es.Skipped, rel)
			}
			sort.Strings(es.Skipped)
			s.Entries = append(s.Entries, es)
		}
		for _, p := range e.peers {
			s.Peers = append(s.Peers, PeerStatus{ID: p.id, Name: p.name})
		}
		for _, f := range e.st.Files {
			if !f.V.IsDeleted() {
				s.Files++
			}
		}
		s.Conflicts = e.conflictCopies()
	})
	sort.Slice(s.Entries, func(i, j int) bool { return s.Entries[i].Path < s.Entries[j].Path })
	sort.Slice(s.Peers, func(i, j int) bool { return s.Peers[i].Name < s.Peers[j].Name })
	s.Sent, s.Received = e.sent.Load(), e.received.Load()
	return s
}

// conflictCopies lists the conflict copies on disk (SPEC §7): tracked ones
// inside folder entries, and those beside single-file entries, which are
// outside every entry. A copy the user deleted is resolved.
func (e *Engine) conflictCopies() []string {
	var out []string
	isFile := func(rel string) bool {
		info, err := fsx.Stat(e.abs(rel))
		return err == nil && info.Kind == fsx.Regular
	}
	for rel, fs := range e.st.Files {
		if _, ok := ParseConflictName(rel); ok && !fs.V.IsDeleted() && isFile(rel) {
			out = append(out, rel)
		}
	}
	for entry, r := range e.st.Entries {
		if r.Removed || r.Dir {
			continue
		}
		names, _ := os.ReadDir(filepath.Dir(e.abs(entry)))
		for _, n := range names {
			rel := path.Join(path.Dir(entry), n.Name())
			if c, ok := ParseConflictName(rel); ok && c.Original == entry && isFile(rel) {
				out = append(out, rel)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ---- paths ------------------------------------------------------------------

// rel maps an absolute path to a slash-separated path relative to ~,
// under the resolved home or the home as given.
func (e *Engine) rel(abs string) (string, bool) {
	for _, home := range []string{e.o.Home, e.givenHome} {
		r, err := filepath.Rel(home, filepath.Clean(abs))
		if err == nil && r != "." && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(r), true
		}
	}
	return "", false
}

func (e *Engine) abs(rel string) string { return filepath.Join(e.o.Home, filepath.FromSlash(rel)) }

// entryFor returns the active entry covering rel.
func (e *Engine) entryFor(rel string) (string, proto.EntryRecord, bool) {
	for path, r := range e.st.Entries {
		if r.Removed {
			continue
		}
		if rel == path || (r.Dir && strings.HasPrefix(rel, path+"/")) {
			return path, r, true
		}
	}
	return "", proto.EntryRecord{}, false
}

// ignored reports whether rel (inside entry path) must not sync.
func (e *Engine) ignored(entry, rel string) bool {
	if rel == entry {
		return false
	}
	m := e.matchers[entry]
	return m != nil && m.Ignored(strings.TrimPrefix(rel, entry+"/"))
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// rootPresent: a folder entry's folder, or a file entry's parent, exists.
func (e *Engine) rootPresent(entry string, r proto.EntryRecord) bool {
	p := e.abs(entry)
	if !r.Dir {
		p = filepath.Dir(p)
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// prune applies the retention policies: history (SPEC §10; each file's
// latest versions are protected, current versions never dropped) and the
// trash (§9, 30 days).
func (e *Engine) prune() {
	protected, current := map[string]bool{}, map[string]bool{}
	for _, fs := range e.st.Files {
		current[fs.V.Hash] = true
		for _, n := range version.Walk(fs.Anc, fs.V.Hash, history.KeepPerFile) {
			protected[n.Hash] = true
		}
	}
	e.hist.Prune(history.DefaultPolicy, protected, current)
	trash.Purge(filepath.Join(e.o.DataDir, "trash"), trash.DefaultMaxAge, time.Now())
}
