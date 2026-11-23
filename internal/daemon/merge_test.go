package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nadavushka-dev/enigma/internal/proto"
)

// The merge setting through the API: set it, read it back in status.
func TestMergeOverTheAPI(t *testing.T) {
	_, a, _ := pair(t)
	f := filepath.Join(a.home, "auth.json")
	os.WriteFile(f, []byte("{}"), 0o644)
	cl := a.client()
	if err := cl.Add(f, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := cl.SetMerge(f, proto.MergeJSON, "expires"); err != nil {
		t.Fatal(err)
	}
	st, err := cl.Status()
	if err != nil {
		t.Fatal(err)
	}
	if e := st.Entries[0]; e.Merge != proto.MergeJSON || e.MergeNewestBy != "expires" {
		t.Fatalf("status entry %+v", e)
	}
	if err := cl.SetMerge(filepath.Join(a.home, "nope"), proto.MergeJSON, ""); err == nil {
		t.Fatal("merge on a non-entry accepted")
	}
}
