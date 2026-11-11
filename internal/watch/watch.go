// Package watch reports changes under a set of roots as debounced batches
// of absolute paths (SPEC §6.1). A root is a directory, watched
// recursively, or a single file, watched through its parent directory.
package watch

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/nadavushka-dev/enigma/internal/fsx"
)

// maxDelay caps how long a busy tree can postpone a batch.
const maxDelay = 2 * time.Second

// Batch is a set of changed paths. Rescan means events may have been lost
// and every root should be rescanned.
type Batch struct {
	Paths  []string
	Rescan bool
}

type root struct {
	file  bool
	skip  func(abs string) bool
	dirs  []string        // directories this root added a watch for
	top   string          // the root folder, or a file root's parent
	added bool            // top's watch was added
	fail  map[string]bool // folders whose watch could not be added
	err   error           // why top's watch could not be added
}

// addHook, when set, runs before each folder watch is added; an error it
// returns is that watch's failure (tests: the watch limit).
var addHook atomic.Pointer[func(dir string) error]

// SetAddHook is for tests: f runs before each folder watch is added, and
// an error it returns fails that watch (as the inotify watch limit,
// ENOSPC, does). It returns a func that removes the hook.
func SetAddHook(f func(dir string) error) (restore func()) {
	addHook.Store(&f)
	return func() { addHook.Store(nil) }
}

type Watcher struct {
	fw       *fsnotify.Watcher
	debounce time.Duration
	out      chan Batch
	done     chan struct{}
	stopped  chan struct{}

	mu      sync.Mutex
	roots   map[string]*root
	watched map[string]int // dir -> number of roots using it
}

func New(debounce time.Duration) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{fw: fw, debounce: debounce, out: make(chan Batch, 16), done: make(chan struct{}),
		stopped: make(chan struct{}), roots: map[string]*root{}, watched: map[string]int{}}
	go w.loop()
	return w, nil
}

func (w *Watcher) Events() <-chan Batch { return w.out }

func (w *Watcher) Close() error {
	close(w.done)
	err := w.fw.Close()
	<-w.stopped
	return err
}

// Add starts watching path. For a directory root, skip (may be nil) prunes
// subdirectories. The root directory, or a file root's parent, is created if
// missing.
func (w *Watcher) Add(path string, file bool, skip func(abs string) bool) error {
	path = filepath.Clean(path)
	dir := path
	if file {
		dir = filepath.Dir(path)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.roots[path]; ok {
		return nil
	}
	r := &root{file: file, skip: skip, top: dir, fail: map[string]bool{}}
	w.roots[path] = r
	if file {
		return w.watchDir(r, dir)
	}
	if err := w.addTree(r, dir); err != nil {
		return err
	}
	if !r.added {
		return fmt.Errorf("watch %s: %w", dir, r.err)
	}
	return nil
}

// Remove stops watching path.
func (w *Watcher) Remove(path string) {
	path = filepath.Clean(path)
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.roots[path]
	if !ok {
		return
	}
	delete(w.roots, path)
	for _, d := range r.dirs {
		if w.watched[d]--; w.watched[d] <= 0 {
			delete(w.watched, d)
			_ = w.fw.Remove(d)
		}
	}
}

// watchDir adds a watch for dir, shared between roots. A failure is
// remembered (Health) until a later add of dir succeeds.
func (w *Watcher) watchDir(r *root, dir string) error {
	if w.watched[dir] == 0 {
		err := error(nil)
		if hook := addHook.Load(); hook != nil {
			err = (*hook)(dir)
		}
		if err == nil {
			err = w.fw.Add(dir)
		}
		if err != nil {
			r.fail[dir] = true
			if dir == r.top {
				r.err = err
			}
			return err
		}
	}
	w.watched[dir]++
	r.dirs = append(r.dirs, dir)
	delete(r.fail, dir)
	if dir == r.top {
		r.added = true
	}
	return nil
}

func (w *Watcher) addTree(r *root, top string) error {
	return filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == top {
				return err
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if p != top && r.skip != nil && r.skip(p) {
			return filepath.SkipDir
		}
		if !contains(r.dirs, p) {
			_ = w.watchDir(r, p)
		}
		return nil
	})
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// owner finds the root responsible for p.
func (w *Watcher) owner(p string) (string, *root) {
	for rp, r := range w.roots {
		if r.file {
			if p == rp {
				return rp, r
			}
			continue
		}
		if p == rp || strings.HasPrefix(p, rp+string(filepath.Separator)) {
			return rp, r
		}
	}
	return "", nil
}

// Health is how well a root is watched.
type Health struct {
	// Added: the root's folder (a file root's parent) got a watch. Alive:
	// that watch is still live; a watch dies with its folder (deleted),
	// and then nothing under it is reported until it is added again. A
	// watch never added did not die: the folder is not another one.
	Added, Alive bool
	// Failed: folders of the root whose watch could not be added (the
	// inotify watch limit), sorted; the root's own folder included.
	Failed []string
}

// Health reports every root's Health, with one look at the watch list.
func (w *Watcher) Health() map[string]Health {
	live := map[string]bool{}
	for _, p := range w.fw.WatchList() {
		live[p] = true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]Health, len(w.roots))
	for p, r := range w.roots {
		h := Health{Added: r.added, Alive: r.added && live[r.top]}
		for d := range r.fail {
			h.Failed = append(h.Failed, d)
		}
		sort.Strings(h.Failed)
		out[p] = h
	}
	return out
}

// Watching reports whether the folder root path is still watched: a
// watch dies with its folder (deleted), and then nothing under the path
// is reported until it is added again.
func (w *Watcher) Watching(path string) bool {
	path = filepath.Clean(path)
	for _, p := range w.fw.WatchList() {
		if p == path {
			return true
		}
	}
	return false
}

func (w *Watcher) handle(ev fsnotify.Event) bool {
	if strings.HasPrefix(filepath.Base(ev.Name), fsx.TempPrefix) {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	rp, r := w.owner(ev.Name)
	if r == nil {
		return false
	}
	if !r.file && ev.Name == rp {
		// The folder root itself went away (deleted or moved): reported,
		// so the engine sees the folder at the path is another one.
		return ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename)
	}
	if r.skip != nil && !r.file && r.skip(ev.Name) {
		return false
	}
	if !r.file && ev.Has(fsnotify.Create) {
		if fi, err := os.Lstat(ev.Name); err == nil && fi.IsDir() {
			_ = w.addTree(r, ev.Name)
		}
	}
	return true
}

func (w *Watcher) loop() {
	defer close(w.stopped)
	pending := map[string]bool{}
	rescan := false
	var quiet, deadline <-chan time.Time
	flush := func() {
		quiet, deadline = nil, nil
		if len(pending) == 0 && !rescan {
			return
		}
		b := Batch{Rescan: rescan}
		for p := range pending {
			b.Paths = append(b.Paths, p)
		}
		sort.Strings(b.Paths)
		pending, rescan = map[string]bool{}, false
		select {
		case w.out <- b:
		case <-w.done:
		}
	}
	arm := func() {
		quiet = time.After(w.debounce)
		if deadline == nil {
			deadline = time.After(maxDelay)
		}
	}
	for {
		select {
		case <-w.done:
			return
		case ev, ok := <-w.fw.Events:
			if !ok {
				return
			}
			if w.handle(ev) {
				pending[ev.Name] = true
				arm()
			}
		case _, ok := <-w.fw.Errors:
			if !ok {
				return
			}
			rescan = true // most likely an overflow
			arm()
		case <-quiet:
			flush()
		case <-deadline:
			flush()
		}
	}
}
