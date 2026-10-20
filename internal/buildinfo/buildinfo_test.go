package buildinfo

import "testing"

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v1.2.3", "v1.2.3", 0, true},
		{"v1.2.3", "v1.2.4", -1, true},
		{"v1.10.0", "v1.9.9", 1, true},
		{"v2.0.0", "v1.99.99", 1, true},
		{"v1.2.3-rc.1", "v1.2.3", -1, true}, // a pre-release is older than its release
		// Dev builds order by commit time, then are older than any release
		// with a higher base.
		{"v0.0.0-dev.20261020143000.abc1234", "v0.0.0-dev.20261021090000.def5678", -1, true},
		{"v0.0.0-dev.20261020143000.abc1234", "v0.0.0-dev.20261020143000.abc1234", 0, true},
		{"v0.0.0-dev.20261020143000.abc1234", "v0.1.0", -1, true},
		// Same commit time, different commits: equal rank, still different
		// versions; nothing to upgrade to, so not ordered.
		{"v0.0.0-dev.20261020143000.abc1234", "v0.0.0-dev.20261020143000.fff0000", 0, false},
		{"dev", "dev", 0, true},
		{"dev", "v1.0.0", 0, false},
		{"garbage", "v1.0.0", 0, false},
	} {
		got, ok := Compare(c.a, c.b)
		if got != c.want || ok != c.ok {
			t.Errorf("Compare(%q, %q) = %d,%v want %d,%v", c.a, c.b, got, ok, c.want, c.ok)
		}
		if back, ok2 := Compare(c.b, c.a); back != -c.want || ok2 != c.ok {
			t.Errorf("Compare(%q, %q) not antisymmetric: %d,%v", c.b, c.a, back, ok2)
		}
	}
}

func TestPlatform(t *testing.T) {
	if p := Platform(); p == "" || p[0] == '/' {
		t.Fatalf("platform %q", p)
	}
}

func TestPlatformFromUname(t *testing.T) {
	for in, want := range map[string]string{
		"Linux x86_64":   "linux/amd64",
		"Linux aarch64":  "linux/arm64",
		"Darwin arm64":   "darwin/arm64",
		"Darwin x86_64":  "darwin/amd64",
		"Linux armv7l":   "",
		"FreeBSD amd64":  "",
		"Linux x86_64\n": "linux/amd64",
	} {
		if got := PlatformFromUname(in); got != want {
			t.Errorf("%q -> %q want %q", in, got, want)
		}
	}
}
