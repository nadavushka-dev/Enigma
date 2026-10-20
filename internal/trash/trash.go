// Package trash is the per-machine trash for files deleted by a synced
// delete (SPEC §9). A trashed file lives at <dir>/<slot>/<path from ~>,
// where slot is the day it was trashed (YYYY-MM-DD, or YYYY-MM-DD.N when
// that day already holds the same path). Its ID is <slot>/<path>.
package trash

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
)

const DefaultMaxAge = 30 * 24 * time.Hour

const dayFormat = "2006-01-02"

type Item struct {
	ID      string    `json:"id"`
	Path    string    `json:"path"` // relative to ~
	Day     time.Time `json:"day"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
}

// Move puts the file src (rel from ~) in the trash, provided it still has
// hash expect.
func Move(dir, src, rel, expect string, now time.Time) (Item, error) {
	day := now.Format(dayFormat)
	for i := 1; i < 1000; i++ {
		slot := day
		if i > 1 {
			slot = day + "." + strconv.Itoa(i)
		}
		dst := filepath.Join(dir, slot, filepath.FromSlash(rel))
		if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
			continue // taken
		}
		if os.MkdirAll(filepath.Dir(dst), 0o700) != nil {
			continue // a trashed file has the name of a folder we need
		}
		final, err := fsx.MoveTo(src, dst, expect)
		if err != nil {
			return Item{}, err
		}
		if final != dst { // raced: MoveTo picked another name
			return Item{}, fmt.Errorf("trash: %s moved to %s", src, final)
		}
		return Item{ID: slot + "/" + rel, Path: rel}, nil
	}
	return Item{}, fmt.Errorf("trash: no free slot for %s", rel)
}

func parseSlot(slot string) (time.Time, bool) {
	day, n, hasN := strings.Cut(slot, ".")
	if hasN {
		if _, err := strconv.Atoi(n); err != nil {
			return time.Time{}, false
		}
	}
	t, err := time.ParseInLocation(dayFormat, day, time.Local)
	return t, err == nil
}

// List returns the trashed files, newest day first.
func List(dir string) ([]Item, error) {
	slots, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, s := range slots {
		day, ok := parseSlot(s.Name())
		if !ok || !s.IsDir() {
			continue
		}
		root := filepath.Join(dir, s.Name())
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil || !info.Mode().IsRegular() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			out = append(out, Item{ID: s.Name() + "/" + rel, Path: rel, Day: day, Size: info.Size(), ModTime: info.ModTime()})
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Day.Equal(out[j].Day) {
			return out[i].Day.After(out[j].Day)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Restore moves a trashed file back to ~/<path>. It never overwrites; the
// restored file then syncs as a normal edit. It returns where it went.
func Restore(dir, home, id string) (string, error) {
	slot, rel, ok := strings.Cut(id, "/")
	if _, valid := parseSlot(slot); !ok || !valid || rel == "" || path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return "", fmt.Errorf("%q is not a trash id (see `enigma trash list`)", id)
	}
	src := filepath.Join(dir, slot, filepath.FromSlash(rel))
	info, err := os.Lstat(src)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not in the trash (see `enigma trash list`)", id)
	}
	dst := filepath.Join(home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	// link + remove: fails instead of replacing a file that exists.
	if err := os.Link(src, dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("~/%s already exists; move it away first", rel)
		}
		return "", err
	}
	os.Remove(src)
	fsx.RemoveEmptyDirs(filepath.Dir(src), dir)
	return dst, nil
}

// Purge removes the days older than maxAge.
func Purge(dir string, maxAge time.Duration, now time.Time) {
	slots, _ := os.ReadDir(dir)
	for _, s := range slots {
		day, ok := parseSlot(s.Name())
		// A day is over at its end: keep it maxAge past that.
		if ok && now.Sub(day.AddDate(0, 0, 1)) > maxAge {
			os.RemoveAll(filepath.Join(dir, s.Name()))
		}
	}
}

// Empty removes everything in the trash and reports how many files went.
func Empty(dir string) (int, error) {
	items, err := List(dir)
	if err != nil {
		return 0, err
	}
	slots, _ := os.ReadDir(dir)
	for _, s := range slots {
		if _, ok := parseSlot(s.Name()); ok {
			if err := os.RemoveAll(filepath.Join(dir, s.Name())); err != nil {
				return 0, err
			}
		}
	}
	return len(items), nil
}
