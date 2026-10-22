package fsx

import (
	"io/fs"
	"syscall"
)

// ident is a file's inode and ctime (unix nanos), or zeros if unknown.
func ident(fi fs.FileInfo) (ino uint64, ctime int64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino, st.Ctimespec.Nano()
	}
	return 0, 0
}
