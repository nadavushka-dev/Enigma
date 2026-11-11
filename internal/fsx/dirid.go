package fsx

import (
	"os"
	"syscall"
)

// DirID identifies a real folder, so a folder put in the place of another
// (moved away and re-created, replaced, remounted) is told apart from the
// one that was there (SPEC §6.5). The inode alone is not enough: Linux
// hands a re-created folder the inode of one just deleted, so the birth
// time is part of it (0 where the file system does not keep one).
type DirID struct {
	Dev   uint64 `json:"dev"`
	Ino   uint64 `json:"ino"`
	Btime int64  `json:"btime,omitempty"` // unix nanoseconds
}

// DirIDOf is p's identity; ok is false for anything that is not a folder,
// a symlink included.
func DirIDOf(p string) (DirID, bool) {
	fi, err := os.Lstat(p)
	if err != nil || !fi.IsDir() {
		return DirID{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return DirID{}, false
	}
	return DirID{Dev: uint64(st.Dev), Ino: st.Ino, Btime: btime(st, p)}, true
}

// SameFolder reports whether a and b identify the same folder. A device
// number alone may change on its own (btrfs subvolumes, NFS, external
// volumes): the same inode and birth time on another device is the same
// folder. Without a birth time on either side (NFSv3), the same inode is
// the same folder whatever the device: a device number drifting is far
// more common than another folder with the same inode on another device.
func SameFolder(a, b DirID) bool {
	if a.Ino != b.Ino {
		return false
	}
	if a.Btime != 0 && b.Btime != 0 {
		return a.Btime == b.Btime
	}
	return true
}
