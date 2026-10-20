// Package watch reports changes under a set of roots as debounced batches
// of absolute paths (SPEC §6.1). A root is a directory, watched
// recursively, or a single file, watched through its parent directory.
package watch

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	file bool
	skip func(abs string) bool
	dirs []string // directories this root added a watch for
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
	r := &root{file: file, skip: skip}
	w.roots[path] = r
	if file {
		return w.watchDir(r, dir)
	}
	return w.addTree(r, dir)
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

func (w *Watcher) watchDir(r *root, dir string) error {
	if w.watched[dir] == 0 {
		if err := w.fw.Add(dir); err != nil {
			return err
		}
	}
	w.watched[dir]++
	r.dirs = append(r.dirs, dir)
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
		if strings.HasPrefix(p, rp+string(filepath.Separator)) {
			return rp, r
		}
	}
	return "", nil
}

func (w *Watcher) handle(ev fsnotify.Event) bool {
	if strings.HasPrefix(filepath.Base(ev.Name), fsx.TempPrefix) {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, r := w.owner(ev.Name)
	if r == nil {
		return false
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
