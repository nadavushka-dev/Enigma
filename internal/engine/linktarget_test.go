package engine

import "testing"

func TestNormalizeLinkTarget(t *testing.T) {
	homes := []string{"/private/var/home/x", "/var/home/x"} // resolved, then as given
	for _, c := range []struct {
		name, link, raw string
		stored, target  string // target "" means refused
	}{
		{"absolute under ~", ".agents/skills/foo", "/var/home/x/repo/skills/foo", "~/repo/skills/foo", "repo/skills/foo"},
		{"absolute under the resolved ~", ".agents/skills/foo", "/private/var/home/x/repo/foo", "~/repo/foo", "repo/foo"},
		{"absolute, trailing slash and dots cleaned", ".agents/skills/foo", "/var/home/x/repo/./a/../foo/", "~/repo/foo", "repo/foo"},
		{"relative inside ~ kept verbatim", ".agents/skills/foo", "../../repo/skills/foo", "../../repo/skills/foo", "repo/skills/foo"},
		{"relative sibling", ".agents/skills/foo", "bar", "bar", ".agents/skills/bar"},
		{"relative with a trailing slash", ".agents/skills/foo", "../../repo/foo/", "../../repo/foo/", "repo/foo"},
		{"relative named ~ is not home", "d/l", "~/x", "./~/x", "d/~/x"},
		{"relative ./~ stays as is", "d/l", "./~/x", "./~/x", "d/~/x"},
		{"relative escaping ~", ".agents/skills/foo", "../../../outside", "", ""},
		{"relative .. games", "a/b", "c/../../../x", "", ""},
		{"absolute outside ~", ".agents/skills/foo", "/etc/passwd", "", ""},
		{"a Mac home on a Linux machine is outside", ".agents/skills/foo", "/Users/x/repo/foo", "", ""},
		{"a sibling home is outside", "l", "/var/home/xy/repo", "", ""},
		{"empty", "l", "", "", ""},
		{"NUL", "l", "a\x00b", "", ""},
		{"~ itself", "l", "/var/home/x", "~", "."},
	} {
		stored, target, err := storeLinkTarget(homes, c.link, c.raw)
		if c.target == "" {
			if err == nil {
				t.Errorf("%s: stored %q, want refused", c.name, stored)
			}
			continue
		}
		if err != nil || stored != c.stored || target != c.target {
			t.Errorf("%s: (%q, %q, %v), want (%q, %q)", c.name, stored, target, err, c.stored, c.target)
			continue
		}
		// Materializing on another machine and reading it back there gives
		// the same stored form: no echo between different homes.
		for _, home := range []string{"/home/x", "/Users/x"} {
			raw := materializeLinkTarget(home, stored)
			again, target2, err := storeLinkTarget([]string{home}, c.link, raw)
			if err != nil || again != stored || target2 != target {
				t.Errorf("%s on %s: wrote %q, read back (%q, %q, %v)", c.name, home, raw, again, target2, err)
			}
		}
		if got, ok := resolveStoredTarget(c.link, stored); !ok || got != target {
			t.Errorf("%s: resolveStoredTarget = (%q, %v), want %q", c.name, got, ok, target)
		}
	}
	for _, bad := range []string{"", "/etc", "~/../x", "../../../../x", "a\x00b"} {
		if got, ok := resolveStoredTarget(".agents/skills/foo", bad); ok {
			t.Errorf("resolveStoredTarget(%q) = %q, want refused", bad, got)
		}
	}
}
