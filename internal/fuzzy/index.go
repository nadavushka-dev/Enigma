package fuzzy

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"

	"github.com/nadavushka-dev/enigma/internal/fsx"
)

// Defaults of the home index (SPEC §12.4).
const (
	DefaultMaxDepth = 8
	DefaultMaxPaths = 50000
)

// skipNames are never descended into, on top of the always-ignored names
// (SPEC §8).
var skipNames = []string{"Library", ".cache", ".Trash"}

// Entry is one path of the index, slash-separated and relative to ~.
type Entry struct {
	Path string
	Dir  bool
}

// Progress is how far the walk got.
type Progress struct {
	Count     int
	Done      bool
	Truncated bool // stopped at MaxPaths
}

// Options tune the walk; zero values mean the defaults.
type Options struct {
	MaxDepth int
	MaxPaths int
	Skip     []string // absolute folders never entered (Enigma's own data folder)
}

// Index is the home index: a background walk of ~ that lists names only
// (no file is read, symlinks are not followed). Snapshot is safe at any
// time, so a UI can show progress while it builds.
type Index struct {
	mu      sync.Mutex
	entries []Entry
	done    bool
	trunc   bool
	stop    chan struct{}
	once    sync.Once
}

// Build starts walking home in the background.
func Build(home string, o Options) *Index {
	if o.MaxDepth <= 0 {
		o.MaxDepth = DefaultMaxDepth
	}
	if o.MaxPaths <= 0 {
		o.MaxPaths = DefaultMaxPaths
	}
	ix := &Index{stop: make(chan struct{})}
	go ix.walk(home, o)
	return ix
}

// Snapshot is a copy of the entries so far, in walk order, and the progress.
func (ix *Index) Snapshot() ([]Entry, Progress) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return append([]Entry(nil), ix.entries...), Progress{Count: len(ix.entries), Done: ix.done, Truncated: ix.trunc}
}

// Paths is the entries' paths, folders with a trailing "/".
func Paths(ents []Entry) []string {
	out := make([]string, len(ents))
	for i, e := range ents {
		out[i] = e.Path
		if e.Dir {
			out[i] += "/"
		}
	}
	return out
}

// Stop ends the walk early; the index stays usable with what it has.
func (ix *Index) Stop() { ix.once.Do(func() { close(ix.stop) }) }

func (ix *Index) walk(home string, o Options) {
	defer func() {
		ix.mu.Lock()
		ix.done = true
		ix.mu.Unlock()
	}()
	ignored := fsx.NewMatcher(nil)
	skip := map[string]bool{}
	for _, s := range o.Skip {
		skip[filepath.Clean(s)] = true
	}
	for _, n := range skipNames {
		skip[filepath.Join(home, n)] = true
	}
	var visit func(rel string, depth int) bool
	visit = func(rel string, depth int) bool {
		select {
		case <-ix.stop:
			return false
		default:
		}
		ents, err := os.ReadDir(filepath.Join(home, filepath.FromSlash(rel)))
		if err != nil {
			return true
		}
		sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
		for _, e := range ents {
			crel := path.Join(rel, e.Name())
			if ignored.Ignored(e.Name()) || skip[filepath.Join(home, filepath.FromSlash(crel))] {
				continue
			}
			dir := e.Type().IsDir() // a symlink is listed, never followed
			ix.mu.Lock()
			if len(ix.entries) >= o.MaxPaths {
				ix.trunc = true
				ix.mu.Unlock()
				return false
			}
			ix.entries = append(ix.entries, Entry{Path: crel, Dir: dir})
			ix.mu.Unlock()
			if dir && depth < o.MaxDepth {
				if !visit(crel, depth+1) {
					return false
				}
			}
		}
		return true
	}
	visit("", 1)
}
