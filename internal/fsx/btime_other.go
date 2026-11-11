//go:build linux && !amd64 && !arm64

package fsx

import "syscall"

// btime is unknown on the Linux platforms Enigma does not ship for.
func btime(*syscall.Stat_t, string) int64 { return 0 }
