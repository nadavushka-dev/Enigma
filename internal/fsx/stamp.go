package fsx

import (
	"io/fs"
	"os"
)

// Stamp is what a file looks like to lstat. Content rewritten in place
// changes the mtime and the ctime (which no tool can set back), a
// replaced file changes the inode, and chmod changes the ctime, so while
// the stamp holds, so does the last hash taken of the file (SPEC §6.1).
type Stamp struct {
	Size  int64       `json:"size"`
	Mtime int64       `json:"mtime"`
	Ctime int64       `json:"ctime"`
	Ino   uint64      `json:"ino"`
	Mode  fs.FileMode `json:"mode"`
}

// StampOf is the stamp of a regular file at p (ok false for anything else).
func StampOf(p string) (Stamp, bool) {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return Stamp{}, false
	}
	ino, ctime := ident(fi)
	return Stamp{Size: fi.Size(), Mtime: fi.ModTime().UnixNano(), Ctime: ctime, Ino: ino, Mode: fi.Mode()}, true
}
