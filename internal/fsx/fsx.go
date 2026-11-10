// Package fsx holds the filesystem primitives Enigma builds on: hashing,
// stat, precondition-checked atomic writes and moves, ignore rules and
// scanning (SPEC §6.6, §8).
package fsx

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// TempPrefix marks Enigma's in-flight temp files; they are never synced.
const TempPrefix = ".enigma-tmp-"

var (
	// ErrChanged: the target no longer matches what the caller expected.
	ErrChanged = errors.New("file changed underneath")
	// ErrNotRegular: something Enigma does not sync is in the way.
	ErrNotRegular = errors.New("not a regular file")
)

type Kind int

const (
	Missing Kind = iota
	Regular
	Dir
	Other // symlink, socket, device...
)

func (k Kind) String() string {
	return [...]string{"missing", "regular", "dir", "other"}[k]
}

// Info describes a path. Hash, Size, Mtime and Exec are set for Regular
// only. A symlink is Kind Other with Link set, its raw target in Target
// and its own mtime; it is never followed (SPEC §8.1, D23).
type Info struct {
	Kind   Kind
	Hash   string
	Size   int64
	Mtime  int64 // unix nanoseconds
	Exec   bool
	Link   bool
	Target string
}

// LinkHash is the hash of a link version whose content is target (D23).
// It is domain-separated, so it never equals the hash of a file.
func LinkHash(target string) string {
	return Hash([]byte("enigma-link\x00" + target))
}

func Hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Stat inspects p without following symlinks.
func Stat(p string) (Info, error) {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return Info{Kind: Missing}, nil
	}
	if err != nil {
		return Info{}, err
	}
	switch {
	case fi.IsDir():
		return Info{Kind: Dir}, nil
	case fi.Mode()&fs.ModeSymlink != 0:
		t, err := os.Readlink(p)
		if err != nil {
			return Info{Kind: Other}, nil
		}
		return Info{Kind: Other, Link: true, Target: t, Mtime: fi.ModTime().UnixNano()}, nil
	case !fi.Mode().IsRegular():
		return Info{Kind: Other}, nil
	}
	h, err := hashFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Info{Kind: Missing}, nil
	}
	if err != nil {
		return Info{}, err
	}
	return Info{Kind: Regular, Hash: h, Size: fi.Size(), Mtime: fi.ModTime().UnixNano(), Exec: fi.Mode()&0o111 != 0}, nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// WriteOpts are the metadata and precondition for Write. Exactly one of
// ExpectAbsent / ExpectHash describes what must be at the target now.
type WriteOpts struct {
	Mtime        int64
	Exec         bool
	ExpectAbsent bool
	ExpectHash   string
}

func (o WriteOpts) check(cur Info) error {
	switch cur.Kind {
	case Missing:
		if o.ExpectAbsent {
			return nil
		}
	case Regular:
		if !o.ExpectAbsent && cur.Hash == o.ExpectHash {
			return nil
		}
	default:
		return ErrNotRegular
	}
	return ErrChanged
}

// Write atomically replaces p with data: temp file in the same directory,
// fsync, metadata, a last precondition check, rename. Missing parent
// directories are created.
func Write(p string, data []byte, o WriteOpts) (Info, error) {
	cur, err := Stat(p)
	if err != nil {
		return Info{}, err
	}
	if err := o.check(cur); err != nil {
		return cur, err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Info{}, err
	}
	f, err := os.CreateTemp(dir, TempPrefix+"*")
	if err != nil {
		return Info{}, err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if _, err := f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Info{}, err
	}
	mode := fs.FileMode(0o644)
	if cur.Kind == Regular {
		if fi, err := os.Stat(p); err == nil {
			mode = fi.Mode().Perm()
		}
	}
	if o.Exec {
		mode |= (mode & 0o444) >> 2 // executable wherever readable
	} else {
		mode &^= 0o111
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return Info{}, err
	}
	mtime := time.Now()
	if o.Mtime != 0 {
		mtime = time.Unix(0, o.Mtime)
	}
	if err := os.Chtimes(tmp, mtime, mtime); err != nil {
		return Info{}, err
	}
	if err := commit(tmp, p, o); err != nil {
		if errors.Is(err, ErrChanged) || errors.Is(err, ErrNotRegular) {
			cur, _ = Stat(p)
		}
		return cur, err
	}
	return Info{Kind: Regular, Hash: Hash(data), Size: int64(len(data)), Mtime: mtime.UnixNano(), Exec: o.Exec}, nil
}

// beforeCommit is a test hook: it runs just before the target is replaced,
// where a concurrent user write would race us.
var beforeCommit func(target string)

// commit moves tmp onto p only if p is still what o expects. POSIX has no
// compare-and-swap on names, so:
//   - expecting absence: link(tmp, p) fails if anything appeared. Atomic.
//   - expecting a hash: hard-link the current p aside, verify that pre-image,
//     rename over p, then re-verify the pre-image. A write that slipped in
//     place between check and rename is put back and reported as ErrChanged.
//     Only an editor's rename-save landing in the microseconds between the
//     final identity check and our rename remains undetectable.
func commit(tmp, p string, o WriteOpts) error {
	if beforeCommit != nil {
		beforeCommit(p)
	}
	if o.ExpectAbsent {
		if err := os.Link(tmp, p); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return ErrChanged
			}
			return err
		}
		return nil
	}
	pre := tmp + "-pre"
	if err := os.Link(p, pre); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrChanged
		}
		return err
	}
	defer os.Remove(pre)
	if info, err := Stat(pre); err != nil || info.Kind != Regular || info.Hash != o.ExpectHash {
		return ErrChanged
	}
	// p must still be the inode we verified.
	a, err1 := os.Lstat(p)
	b, err2 := os.Lstat(pre)
	if err1 != nil || err2 != nil || !os.SameFile(a, b) {
		return ErrChanged
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	if info, err := Stat(pre); err == nil && info.Hash != o.ExpectHash {
		// Edited in place while we swapped: give the user's bytes back.
		if err := os.Rename(pre, p); err != nil {
			return err
		}
		return ErrChanged
	}
	return nil
}

// LinkOpts is the precondition for WriteLink: exactly one of them
// describes what must be at the target now.
type LinkOpts struct {
	ExpectAbsent bool
	ExpectHash   string // a regular file with this content
	ExpectLink   string // a symlink with this raw target
}

func (o LinkOpts) check(cur Info) error {
	switch {
	case cur.Kind == Missing:
		if o.ExpectAbsent {
			return nil
		}
	case cur.Kind == Regular:
		if o.ExpectHash != "" && cur.Hash == o.ExpectHash {
			return nil
		}
	case cur.Link:
		if o.ExpectLink != "" && cur.Target == o.ExpectLink {
			return nil
		}
	default: // a folder, a socket...: never replaced by a link
		return ErrNotRegular
	}
	return ErrChanged
}

// WriteLink makes p a symlink to target (SPEC §6.6, D23), never following
// one: into an absent name with symlink(2), which fails if anything
// appeared; over a file or a link by renaming a new link from the same
// folder onto it, after a last precondition check. A folder is never
// replaced. Missing parent directories are created.
func WriteLink(p, target string, o LinkOpts) error {
	if target == "" || strings.ContainsRune(target, 0) {
		return fmt.Errorf("bad link target %q", target)
	}
	cur, err := Stat(p)
	if err != nil {
		return err
	}
	if err := o.check(cur); err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if o.ExpectAbsent {
		if beforeCommit != nil {
			beforeCommit(p)
		}
		if err := os.Symlink(target, p); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return ErrChanged
			}
			return err
		}
		return nil
	}
	var tmp string
	for i := 0; ; i++ {
		tmp = filepath.Join(dir, fmt.Sprintf("%slink-%d-%d", TempPrefix, time.Now().UnixNano(), i))
		err := os.Symlink(target, tmp)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || i > 100 {
			return err
		}
	}
	defer os.Remove(tmp) // no-op after a successful rename
	if o.ExpectHash != "" {
		return commit(tmp, p, WriteOpts{ExpectHash: o.ExpectHash})
	}
	if beforeCommit != nil {
		beforeCommit(p)
	}
	if t, err := os.Readlink(p); err != nil || t != o.ExpectLink {
		return ErrChanged
	}
	return os.Rename(tmp, p)
}

// MoveLinkTo moves the symlink src itself (never what it points at) to
// dst, provided its raw target is still expect. Like MoveTo, it never
// overwrites: a taken dst gets a numeric suffix. It returns the final
// destination.
func MoveLinkTo(src, dst, expect string) (string, error) {
	cur, err := Stat(src)
	if err != nil {
		return "", err
	}
	switch {
	case cur.Kind == Missing:
		return "", ErrChanged
	case !cur.Link:
		return "", ErrNotRegular
	case cur.Target != expect:
		return "", ErrChanged
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	final := freeName(dst)
	if err := os.Rename(src, final); err != nil {
		return "", err
	}
	return final, nil
}

// freeName is dst, or dst with a numeric suffix if dst is taken.
func freeName(dst string) string {
	final := dst
	for i := 2; ; i++ {
		if _, err := os.Lstat(final); errors.Is(err, fs.ErrNotExist) {
			return final
		}
		ext := filepath.Ext(dst)
		final = fmt.Sprintf("%s.%d%s", strings.TrimSuffix(dst, ext), i, ext)
	}
}

// MoveTo moves the regular file src to dst (creating dst's directories),
// provided src still has hash expect. If dst exists, a numeric suffix is
// added so nothing is overwritten. It returns the final destination.
func MoveTo(src, dst, expect string) (string, error) {
	cur, err := Stat(src)
	if err != nil {
		return "", err
	}
	if cur.Kind != Regular {
		return "", ErrNotRegular
	}
	if cur.Hash != expect {
		return "", ErrChanged
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	final := freeName(dst)
	if err := os.Rename(src, final); err != nil {
		return "", err
	}
	return final, nil
}

// RemoveEmptyDirs removes dir and its parents while they are empty, never
// going above or removing root.
func RemoveEmptyDirs(dir, root string) {
	root = filepath.Clean(root)
	for d := filepath.Clean(dir); d != root && strings.HasPrefix(d, root+string(filepath.Separator)); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			return
		}
	}
}

// SymlinkedParent reports whether a folder on the way from root (included)
// down to p's parent is a symlink. Lstat only refuses to follow the last
// component of a path, so a path under such a folder reads and writes
// whatever the link points at; Enigma never touches it (SPEC §6.6, D15).
// A component that is missing or not a folder ends the check: nothing
// below it exists.
func SymlinkedParent(root, p string) bool {
	root, p = filepath.Clean(root), filepath.Clean(p)
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	dir := root
	parts := strings.Split(rel, string(filepath.Separator))
	for i := 0; ; i++ {
		fi, err := os.Lstat(dir)
		if err != nil {
			return false
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return true
		}
		if !fi.IsDir() || i >= len(parts)-1 {
			return false
		}
		dir = filepath.Join(dir, parts[i])
	}
}
