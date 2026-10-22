package fuzzy

import (
	"fmt"
	"reflect"
	"testing"
)

func mustMatch(t *testing.T, q, s string) Result {
	t.Helper()
	r, ok := Match(q, s)
	if !ok {
		t.Fatalf("Match(%q, %q) = no match", q, s)
	}
	return r
}

func TestMatchCharactersInOrder(t *testing.T) {
	mustMatch(t, "ntd", "notes/todo.md")
	mustMatch(t, "", "anything")
	for _, bad := range []string{"xyz", "dtn", "notes/todo.mdx"} {
		if _, ok := Match(bad, "notes/todo.md"); ok {
			t.Fatalf("%q must not match notes/todo.md", bad)
		}
	}
}

func TestMatchSmartCase(t *testing.T) {
	mustMatch(t, "readme", "README.md")
	mustMatch(t, "README", "README.md")
	if _, ok := Match("Readme", "readme.md"); ok {
		t.Fatal("an upper case letter in the query makes it case sensitive")
	}
}

func TestMatchSpacesSplitTerms(t *testing.T) {
	r := mustMatch(t, "notes md", "notes/todo.md")
	if !reflect.DeepEqual(r.Positions, []int{0, 1, 2, 3, 4, 11, 12}) {
		t.Fatalf("positions %v", r.Positions)
	}
	r = mustMatch(t, "md notes", "notes/todo.md") // order of terms does not matter
	if !reflect.DeepEqual(r.Positions, []int{0, 1, 2, 3, 4, 11, 12}) {
		t.Fatalf("positions %v", r.Positions)
	}
	if _, ok := Match("notes zz", "notes/todo.md"); ok {
		t.Fatal("every term must match")
	}
	mustMatch(t, "  notes   ", "notes/todo.md")
}

func TestMatchPositionsPreferSegmentStarts(t *testing.T) {
	r := mustMatch(t, "td", "notes/todo.md")
	if !reflect.DeepEqual(r.Positions, []int{6, 8}) {
		t.Fatalf("positions %v, want [6 8]", r.Positions)
	}
	r = mustMatch(t, "todo", "notes/todo.md")
	if !reflect.DeepEqual(r.Positions, []int{6, 7, 8, 9}) {
		t.Fatalf("positions %v", r.Positions)
	}
	if r, _ := Match("", "x"); len(r.Positions) != 0 {
		t.Fatalf("empty query highlights nothing: %v", r.Positions)
	}
}

func score(t *testing.T, q, s string) int { return mustMatch(t, q, s).Score }

func TestScoreRewards(t *testing.T) {
	// consecutive characters
	if score(t, "todo", "notes/todo.md") <= score(t, "todo", "t/o/d/o") {
		t.Fatal("consecutive characters must score higher")
	}
	// characters at the start of a segment (after / . _ -)
	if score(t, "nt", "notes/todo.md") <= score(t, "nt", "another.txt") {
		t.Fatal("segment starts must score higher")
	}
	if score(t, "cm", "claude_memory") <= score(t, "cm", "ocamel") {
		t.Fatal("_ starts a segment")
	}
	if score(t, "cm", "claude-memory") <= score(t, "cm", "ocamel") {
		t.Fatal("- starts a segment")
	}
	// matches in the base name
	if score(t, "md", "zzz/x.md") <= score(t, "md", "x/md/zzz") {
		t.Fatal("the base name must score higher")
	}
}

func TestRankBestFirstTiesToTheShorterPath(t *testing.T) {
	hits := Rank("a", []string{"abc", "ab", "zzz", "a"})
	var got []string
	for _, h := range hits {
		got = append(got, h.Path)
	}
	if !reflect.DeepEqual(got, []string{"a", "ab", "abc"}) {
		t.Fatalf("rank %v", got)
	}
	hits = Rank("todo", []string{"t/o/d/o.txt", "notes/todo.md"})
	if hits[0].Path != "notes/todo.md" || !reflect.DeepEqual(Highlight("todo", hits[0].Path), []int{6, 7, 8, 9}) {
		t.Fatalf("rank %+v", hits)
	}
	if n := len(Rank("", []string{"b", "a"})); n != 2 {
		t.Fatalf("an empty query keeps every path: %d", n)
	}
}

// Lowercasing must not move positions: İ (U+0130) lowercases to two runes.
func TestMatchKeepsPositionsWithMultiRuneLowercase(t *testing.T) {
	r := mustMatch(t, "x", "İstanbul/x")
	if !reflect.DeepEqual(r.Positions, []int{9}) {
		t.Fatalf("positions %v, want [9]", r.Positions)
	}
	if r.Score != score(t, "x", "istanbul/x") {
		t.Fatalf("base-name bonus drifted: %d vs %d", r.Score, score(t, "x", "istanbul/x"))
	}
	mustMatch(t, "i", "İstanbul")
}

func TestHighlightMatchesMatch(t *testing.T) {
	for _, c := range [][2]string{{"td", "notes/todo.md"}, {"notes md", "notes/todo.md"}, {"x", "İstanbul/x"}, {"", "a"}} {
		r := mustMatch(t, c[0], c[1])
		if got := Highlight(c[0], c[1]); !reflect.DeepEqual(got, r.Positions) && !(len(got) == 0 && len(r.Positions) == 0) {
			t.Fatalf("Highlight(%q, %q) = %v, Match says %v", c[0], c[1], got, r.Positions)
		}
	}
	if Highlight("zz", "abc") != nil {
		t.Fatal("no match, no positions")
	}
}

// A scorer reused across paths gives the same scores as a fresh Match.
func TestRankScoresEqualMatch(t *testing.T) {
	paths := []string{"notes/todo.md", "t/o/d/o", "a", "zzz/x.md", "x/md/zzz", "claude_memory", "README.md"}
	for _, q := range []string{"todo", "md", "cm", "a", "readme", "o d"} {
		for _, h := range Rank(q, paths) {
			if r, ok := Match(q, h.Path); !ok || r.Score != h.Score {
				t.Fatalf("Rank(%q) %s = %d, Match %v %d", q, h.Path, h.Score, ok, r.Score)
			}
		}
	}
}

func BenchmarkRank50k(b *testing.B) {
	paths := make([]string, 0, 50000)
	for i := 0; len(paths) < 50000; i++ {
		paths = append(paths, fmt.Sprintf("work/project-%d/src/internal/module_%d/handler_%d.go", i%40, i%97, i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Rank("hndl", paths)
	}
}
