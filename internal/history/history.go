// Package history stores the content of past versions by hash, as merge
// bases (SPEC §7 step 3, §10). It is per machine and never synced.
package history

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
)

// Policy is the retention rule: a blob is kept while protected (among the
// last versions of some file) or younger than MaxAge; above MaxBytes the
// oldest go first, except current versions.
type Policy struct {
	MaxAge   time.Duration
	MaxBytes int64
}

var DefaultPolicy = Policy{MaxAge: 30 * 24 * time.Hour, MaxBytes: 200 << 20}

// KeepPerFile is how many of a file's latest versions are protected.
const KeepPerFile = 20

type Store struct{ dir string }

func Open(dir string) *Store { return &Store{dir: dir} }

func (s *Store) path(hash string) string {
	return filepath.Join(s.dir, "objects", hash[:min(2, len(hash))], hash)
}

// Put stores data and returns its hash. Storing it again refreshes its age.
func (s *Store) Put(data []byte) (string, error) {
	h := fsx.Hash(data)
	p := s.path(h)
	if _, err := os.Stat(p); err == nil {
		now := time.Now()
		os.Chtimes(p, now, now)
		return h, nil
	}
	if _, err := fsx.Write(p, data, fsx.WriteOpts{Mtime: time.Now().UnixNano(), ExpectAbsent: true}); err != nil && !errors.Is(err, fsx.ErrChanged) {
		return "", err
	}
	return h, nil
}

// Get returns the content for hash, if stored intact.
func (s *Store) Get(hash string) ([]byte, bool) {
	if hash == "" {
		return nil, false
	}
	b, err := os.ReadFile(s.path(hash))
	if err != nil || fsx.Hash(b) != hash {
		return nil, false
	}
	return b, true
}

// Prune applies the policy. protected are the hashes among each file's
// latest versions; current are the current versions, never dropped.
func (s *Store) Prune(pol Policy, protected, current map[string]bool) {
	type blob struct {
		path, hash string
		size       int64
		mod        time.Time
	}
	var keep []blob
	var total int64
	now := time.Now()
	filepath.WalkDir(filepath.Join(s.dir, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if strings.HasPrefix(d.Name(), fsx.TempPrefix) {
			if now.Sub(info.ModTime()) > time.Hour { // left by a crash
				os.Remove(p)
			}
			return nil
		}
		h := d.Name()
		if !protected[h] && !current[h] && now.Sub(info.ModTime()) > pol.MaxAge {
			os.Remove(p)
			return nil
		}
		keep = append(keep, blob{p, h, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	sort.Slice(keep, func(i, j int) bool { return keep[i].mod.Before(keep[j].mod) })
	for _, b := range keep {
		if total <= pol.MaxBytes {
			break
		}
		if current[b.hash] {
			continue
		}
		if os.Remove(b.path) == nil {
			total -= b.size
		}
	}
}
