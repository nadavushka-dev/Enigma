package main

import (
	"os"
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

func TestD23_EntryLinksNeedsOnOrOff(t *testing.T) {
	for _, args := range [][]string{{"entry", "links", "~/x"}, {"entry", "links", "~/x", "maybe"}} {
		var out strings.Builder
		if code := run(args, &ctx{stdout: &out, stderr: &out, stdin: strings.NewReader("")}); code != 2 {
			t.Fatalf("%v: exit %d (%s)", args, code, out.String())
		}
	}
}
