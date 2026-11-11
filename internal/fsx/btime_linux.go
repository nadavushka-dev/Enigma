//go:build linux && (amd64 || arm64)

package fsx

import (
	"syscall"
	"unsafe"
)

// statx(2), from the kernel ABI (include/uapi/linux/stat.h): 256 bytes.
type statxTimestamp struct {
	Sec  int64
	Nsec uint32
	_    int32
}

type statxT struct {
	Mask           uint32
	Blksize        uint32
	Attributes     uint64
	Nlink          uint32
	UID            uint32
	GID            uint32
	Mode           uint16
	_              uint16
	Ino            uint64
	Size           uint64
	Blocks         uint64
	AttributesMask uint64
	Atime          statxTimestamp
	Btime          statxTimestamp
	Ctime          statxTimestamp
	Mtime          statxTimestamp
	RdevMajor      uint32
	RdevMinor      uint32
	DevMajor       uint32
	DevMinor       uint32
	_              [14]uint64
}

const (
	atFdcwd           = -100
	atSymlinkNofollow = 0x100
	statxBtimeMask    = 0x800
)

// btime is p's birth time in unix nanoseconds, from statx(2); 0 when the
// kernel or the file system does not report one.
func btime(_ *syscall.Stat_t, p string) int64 {
	path, err := syscall.BytePtrFromString(p)
	if err != nil {
		return 0
	}
	var st statxT
	fd := atFdcwd
	_, _, errno := syscall.Syscall6(sysStatx, uintptr(fd), uintptr(unsafe.Pointer(path)),
		atSymlinkNofollow, statxBtimeMask, uintptr(unsafe.Pointer(&st)), 0)
	if errno != 0 || st.Mask&statxBtimeMask == 0 {
		return 0
	}
	return st.Btime.Sec*1e9 + int64(st.Btime.Nsec)
}
