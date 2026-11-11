package fsx

import (
	"os"
	"syscall"
)

// DirID identifies a real folder (device and inode), so a folder put in
// the place of another (moved away and re-created, replaced, remounted)
// is told apart from the one that was there (SPEC §6.5). ok is false for
// anything that is not a folder, a symlink included.
type DirID struct {
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
}

func DirIDOf(p string) (DirID, bool) {
	fi, err := os.Lstat(p)
	if err != nil || !fi.IsDir() {
		return DirID{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return DirID{}, false
	}
	return DirID{Dev: uint64(st.Dev), Ino: st.Ino}, true
}
