package main

import (
	"strings"
	"testing"
)

func TestEntryMergeNeedsAPathAndAMode(t *testing.T) {
	for _, args := range [][]string{
		{"entry", "merge", "~/x"},
		{"entry", "merge", "~/x", "yaml"},
		{"entry", "merge", "~/x", "lines", "--newest-by", "expires"},
		{"entry", "merge", "~/x", "json", "--newest-by"},
		{"entry", "merge", "~/x", "json", "extra"},
	} {
		var out strings.Builder
		if code := run(args, &ctx{stdout: &out, stderr: &out, stdin: strings.NewReader("")}); code != 2 {
			t.Fatalf("%v: exit %d (%s)", args, code, out.String())
		}
	}
}
