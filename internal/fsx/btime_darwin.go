package fsx

import "syscall"

// btime is the birth time of the file st describes, in unix nanoseconds.
func btime(st *syscall.Stat_t, _ string) int64 {
	return st.Birthtimespec.Nano()
}
