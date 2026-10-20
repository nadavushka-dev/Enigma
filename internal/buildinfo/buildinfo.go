// Package buildinfo is this binary's Enigma version and platform. Linked
// machines must run the same version (SPEC §11).
package buildinfo

import (
	"runtime"
	"strconv"
	"strings"
)

// Version is set at build time:
//
//	-ldflags "-X github.com/nadavushka-dev/enigma/internal/buildinfo.Version=v1.2.3"
//
// Releases are semver; dev builds (`make build`) are
// v0.0.0-dev.<commit time YYYYMMDDHHMMSS UTC>.<short hash>. A plain `go build`
// is "dev", which equals only itself.
var Version = "dev"

// Platform is os/arch, as Go names them.
func Platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// PlatformFromUname maps `uname -sm` output to a supported platform, or "".
func PlatformFromUname(s string) string {
	f := strings.Fields(s)
	if len(f) != 2 {
		return ""
	}
	os := map[string]string{"Linux": "linux", "Darwin": "darwin"}[f[0]]
	arch := map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[f[1]]
	if os == "" || arch == "" {
		return ""
	}
	return os + "/" + arch
}

type parsed struct {
	core [3]int
	pre  []string // nil for a release
}

func parse(v string) (parsed, bool) {
	var p parsed
	rest, ok := strings.CutPrefix(v, "v")
	if !ok {
		return p, false
	}
	core, pre, hasPre := strings.Cut(rest, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return p, false
	}
	for i, s := range parts {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return p, false
		}
		p.core[i] = n
	}
	if hasPre {
		if pre == "" {
			return p, false
		}
		p.pre = strings.Split(pre, ".")
	}
	return p, true
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Compare orders two versions: -1 if a is older, 1 if newer, 0 if the same
// rank. ok is false when they cannot be ordered: an unparsable or plain
// "dev" build against anything else, or two dev builds from different
// commits made at the same second. Only an ordered pair can upgrade.
func Compare(a, b string) (int, bool) {
	if a == b {
		return 0, true
	}
	pa, okA := parse(a)
	pb, okB := parse(b)
	if !okA || !okB {
		return 0, false
	}
	for i := range pa.core {
		if c := cmpInt(pa.core[i], pb.core[i]); c != 0 {
			return c, true
		}
	}
	switch {
	case pa.pre == nil && pb.pre == nil:
		return 0, true
	case pa.pre == nil:
		return 1, true // a release beats its pre-releases
	case pb.pre == nil:
		return -1, true
	}
	if isDev(pa.pre) && isDev(pb.pre) {
		if c := strings.Compare(pa.pre[1], pb.pre[1]); c != 0 { // fixed-width timestamps
			return c, true
		}
		return 0, false // same second, different commits
	}
	for i := 0; i < len(pa.pre) && i < len(pb.pre); i++ {
		x, y := pa.pre[i], pb.pre[i]
		nx, errX := strconv.Atoi(x)
		ny, errY := strconv.Atoi(y)
		var c int
		switch {
		case errX == nil && errY == nil:
			c = cmpInt(nx, ny)
		case errX == nil:
			c = -1 // numeric identifiers sort first (semver)
		case errY == nil:
			c = 1
		default:
			c = strings.Compare(x, y)
		}
		if c != 0 {
			return c, true
		}
	}
	return cmpInt(len(pa.pre), len(pb.pre)), true
}

func isDev(pre []string) bool {
	return len(pre) == 3 && pre[0] == "dev" && len(pre[1]) == 14
}
