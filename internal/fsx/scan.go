package fsx

import (
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// DefaultIgnore is applied to every entry (SPEC §8).
var DefaultIgnore = []string{
	".git", ".hg", ".svn", "node_modules", ".DS_Store",
	"*.swp", "*.swx", "*~", ".#*", "#*#", TempPrefix + "*",
}

// Matcher decides which relative paths are ignored. A pattern without "/"
// matches any path component; a pattern with "/" matches from the root.
type Matcher struct {
	names, paths []string
}

func NewMatcher(extra []string) *Matcher {
	m := &Matcher{}
	for _, p := range append(append([]string{}, DefaultIgnore...), extra...) {
		p = strings.Trim(strings.TrimSpace(p), "/")
		switch {
		case p == "":
		case strings.Contains(p, "/"):
			m.paths = append(m.paths, p)
		default:
			m.names = append(m.names, p)
		}
	}
	return m
}

// Ignored reports whether rel (slash-separated) or any parent is ignored.
func (m *Matcher) Ignored(rel string) bool {
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		for _, n := range m.names {
			if ok, _ := path.Match(n, part); ok {
				return true
			}
		}
		prefix := strings.Join(parts[:i+1], "/")
		for _, p := range m.paths {
			if ok, _ := path.Match(p, prefix); ok {
				return true
			}
		}
	}
	return false
}

// ScanResult maps slash-separated paths relative to the root to file info.
// Skipped lists files that exist but are not synced (symlinks, oversized).
type ScanResult struct {
	Files   map[string]Info
	Skipped []string
}

// Scan walks root. Files larger than maxSize (if > 0) are skipped.
func Scan(root string, m *Matcher, maxSize int64) (ScanResult, error) {
	res := ScanResult{Files: map[string]Info{}}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil // unreadable subtree
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if m.Ignored(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			res.Skipped = append(res.Skipped, rel)
			return nil
		}
		if oversized(d, maxSize) { // before hashing: never read what is skipped
			res.Skipped = append(res.Skipped, rel)
			return nil
		}
		info, err := Stat(p)
		if err != nil || info.Kind != Regular {
			return nil
		}
		if maxSize > 0 && info.Size > maxSize { // grew since the walk read its size
			res.Skipped = append(res.Skipped, rel)
			return nil
		}
		res.Files[rel] = info
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return res, nil
	}
	return res, err
}

// CountFiles counts the files Scan would sync under root, without reading
// any, and stops once the count passes limit (returning limit+1).
func CountFiles(root string, m *Matcher, maxSize int64, limit int) (int, error) {
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if m.Ignored(filepath.ToSlash(rel)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || oversized(d, maxSize) {
			return nil
		}
		if n++; n > limit {
			return fs.SkipAll
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	return n, err
}

func oversized(d fs.DirEntry, maxSize int64) bool {
	if maxSize <= 0 {
		return false
	}
	fi, err := d.Info()
	return err == nil && fi.Size() > maxSize
}
