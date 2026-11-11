package main

import (
	"os"

	"github.com/nadavushka-dev/enigma/internal/engine"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestD23_AddWithLinksAsksForLinkReplication(t *testing.T) {
	fa, _, home := pickWorld(t, false, false, nil)
	os.MkdirAll(filepath.Join(home, "skills"), 0o755)
	out, code := runAdd("--links", "~/skills")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if !reflect.DeepEqual(fa.linkAdds, []string{filepath.Join(home, "skills")}) || len(fa.adds) != 0 {
		t.Fatalf("link adds %v, plain adds %v", fa.linkAdds, fa.adds)
	}
	if !strings.Contains(out, "links inside it are replicated") {
		t.Fatalf("output %q", out)
	}
}

// Hand test S7 and F2: status never says "ok" for an entry that is not
// synced here, or not watched.
func TestStatusStateIsNeverOkWhenNotSynced(t *testing.T) {
	lim := engine.Limits{MaxFileSize: 10 << 20, MaxFiles: 2000}
	for _, c := range []struct {
		e    engine.EntryStatus
		want string
	}{
		{engine.EntryStatus{Path: "p", Dir: true, Frozen: true}, "not synced here"},
		{engine.EntryStatus{Path: "p", Dir: true, Unwatched: true}, "NOT WATCHED"},
		{engine.EntryStatus{Path: "p", Dir: true, UnwatchedFolders: []string{"p/a", "p/b"}}, "PARTLY WATCHED: 2 subfolders"},
		{engine.EntryStatus{Path: "p", Dir: true, Restoring: []string{"p/a"}}, "restoring 1"},
		{engine.EntryStatus{Path: "p", Dir: true, WriteFailing: []string{"p/a"}}, "WRITE FAILING"},
		{engine.EntryStatus{Path: "p", Dir: true}, "ok"},
	} {
		if got := entryState(c.e, lim); !strings.HasPrefix(got, c.want) {
			t.Errorf("%+v: %q, want %q", c.e, got, c.want)
		}
	}
}

func TestD23_EntryLinksNeedsOnOrOff(t *testing.T) {
	for _, args := range [][]string{{"entry", "links", "~/x"}, {"entry", "links", "~/x", "maybe"}} {
		var out strings.Builder
		if code := run(args, &ctx{stdout: &out, stderr: &out, stdin: strings.NewReader("")}); code != 2 {
			t.Fatalf("%v: exit %d (%s)", args, code, out.String())
		}
	}
}
