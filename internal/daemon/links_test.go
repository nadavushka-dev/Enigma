package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nadavushka-dev/enigma/internal/proto"
)

// D23 through the API: add --links, then turn it off and on again.
func TestD23_LinksOverTheAPI(t *testing.T) {
	_, a, b := pair(t)
	eventually(t, "linked", connected(a, "b"))
	os.MkdirAll(filepath.Join(a.home, "repo", "foo"), 0o755)
	os.WriteFile(filepath.Join(a.home, "repo", "foo", "SKILL.md"), []byte("foo"), 0o644)
	os.MkdirAll(filepath.Join(a.home, "skills"), 0o755)
	os.Symlink(filepath.Join(a.home, "repo", "foo"), filepath.Join(a.home, "skills", "foo"))

	cl := a.client()
	if err := cl.Add(filepath.Join(a.home, "repo"), nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := cl.AddWithLinks(filepath.Join(a.home, "skills"), nil, "", proto.FirstAddKeepBoth); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(b.home, "repo", "foo")
	eventually(t, "link on b", func() bool {
		got, err := os.Readlink(filepath.Join(b.home, "skills", "foo"))
		return err == nil && got == want
	})
	linksOn := func() bool {
		st, err := cl.Status()
		if err != nil {
			return false
		}
		for _, e := range st.Entries {
			if e.Path == "skills" {
				return e.LinksOn
			}
		}
		return false
	}
	if !linksOn() {
		t.Fatal("status does not say links are on")
	}
	if err := cl.SetLinks(filepath.Join(a.home, "skills"), false); err != nil {
		t.Fatal(err)
	}
	if linksOn() {
		t.Fatal("links still on")
	}
	if err := cl.SetLinks(filepath.Join(a.home, "repo", "foo", "SKILL.md"), true); err == nil {
		t.Fatal("links on a non-entry accepted")
	}
}
