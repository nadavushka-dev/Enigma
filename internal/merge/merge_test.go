package merge

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func lines(ls ...string) []byte {
	if len(ls) == 0 {
		return []byte{}
	}
	return []byte(strings.Join(ls, "\n") + "\n")
}

func TestIsText(t *testing.T) {
	for _, c := range []struct {
		in   []byte
		want bool
	}{
		{[]byte("# rules\n"), true},
		{[]byte{}, true},
		{[]byte("héllo ✓\n"), true},
		{[]byte("a\x00b"), false},
		{[]byte{0xff, 0xfe, 'a'}, false},
		{append(bytes.Repeat([]byte("a"), 9000), 0), true}, // NUL past the first 8 KB
	} {
		if got := IsText(c.in); got != c.want {
			t.Errorf("IsText(%.20q) = %v", c.in, got)
		}
	}
}

func TestIsTextAllowsARuneCutAt8KB(t *testing.T) {
	b := append(bytes.Repeat([]byte("a"), 8191), "✓"...) // a 3-byte rune straddles the limit
	if !IsText(b) {
		t.Fatal("a rune cut by the 8 KB window is still text")
	}
}

type mergeCase struct {
	name         string
	base, a, b   []byte
	want         []byte
	wantConflict bool
}

var cases = []mergeCase{
	{name: "edits far apart merge",
		base: lines("1", "2", "3", "4", "5"),
		a:    lines("1 from a", "2", "3", "4", "5"),
		b:    lines("1", "2", "3", "4", "5 from b"),
		want: lines("1 from a", "2", "3", "4", "5 from b")},
	{name: "one side unchanged takes the other",
		base: lines("1", "2"), a: lines("1", "2"), b: lines("1", "two", "3"),
		want: lines("1", "two", "3")},
	{name: "same change on both sides is no conflict",
		base: lines("1", "2", "3"), a: lines("1", "x", "3"), b: lines("1", "x", "3"),
		want: lines("1", "x", "3")},
	{name: "overlapping edits conflict",
		base: lines("1", "2", "3"), a: lines("1", "a", "3"), b: lines("1", "b", "3"),
		wantConflict: true},
	{name: "touching edits conflict",
		base: lines("1", "2", "3", "4"), a: lines("1", "a", "3", "4"), b: lines("1", "2", "b", "4"),
		wantConflict: true},
	{name: "inserts at the same place conflict",
		base: lines("1", "2"), a: lines("1", "a", "2"), b: lines("1", "b", "2"),
		wantConflict: true},
	{name: "delete on one side, edit elsewhere on the other",
		base: lines("1", "2", "3", "4", "5"), a: lines("1", "3", "4", "5"), b: lines("1", "2", "3", "4", "five"),
		want: lines("1", "3", "4", "five")},
	{name: "append on both ends",
		base: lines("mid"), a: lines("top", "mid"), b: lines("mid", "bottom"),
		want: lines("top", "mid", "bottom")},
	{name: "both create from empty, differently",
		base: []byte{}, a: lines("a"), b: lines("b"),
		wantConflict: true},
	{name: "missing final newline is a change of the last line",
		base: []byte("1\n2\n3"), a: []byte("1 from a\n2\n3"), b: []byte("1\n2\n3\n"),
		want: []byte("1 from a\n2\n3\n")},
	{name: "CRLF lines are kept as they are",
		base: []byte("1\r\n2\r\n3\r\n4\r\n"), a: []byte("one\r\n2\r\n3\r\n4\r\n"), b: []byte("1\r\n2\r\n3\r\nfour\r\n"),
		want: []byte("one\r\n2\r\n3\r\nfour\r\n")},
}

func TestMerge3(t *testing.T) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Merge3(c.base, c.a, c.b)
			if c.wantConflict {
				if ok {
					t.Fatalf("want conflict, got clean %q", got)
				}
				return
			}
			if !ok || !bytes.Equal(got, c.want) {
				t.Fatalf("got %q ok=%v, want %q", got, ok, c.want)
			}
		})
	}
}

// Two machines merge the same pair with the sides swapped; they must make
// byte-identical results, or the conflict would be resolved twice (SPEC §7).
func TestMerge3IsSymmetric(t *testing.T) {
	for _, c := range cases {
		x, ok1 := Merge3(c.base, c.a, c.b)
		y, ok2 := Merge3(c.base, c.b, c.a)
		if ok1 != ok2 || !bytes.Equal(x, y) {
			t.Errorf("%s: asymmetric: %q/%v vs %q/%v", c.name, x, ok1, y, ok2)
		}
	}
}

// Random edits: a clean merge keeps every line each side changed, and the
// result is symmetric. A side merged with an unchanged base is that side.
func TestMerge3Random(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	edit := func(base []string, tag string) []string {
		out := append([]string(nil), base...)
		for n := rng.Intn(3) + 1; n > 0; n-- {
			i := rng.Intn(len(out) + 1)
			switch rng.Intn(3) {
			case 0: // insert
				out = append(out[:i], append([]string{fmt.Sprintf("%s-ins-%d", tag, rng.Int())}, out[i:]...)...)
			case 1: // delete
				if i < len(out) {
					out = append(out[:i], out[i+1:]...)
				}
			default: // change
				if i < len(out) {
					out[i] = fmt.Sprintf("%s-chg-%d", tag, rng.Int())
				}
			}
		}
		return out
	}
	for iter := 0; iter < 2000; iter++ {
		var base []string
		for i := rng.Intn(30); i > 0; i-- {
			base = append(base, fmt.Sprintf("line %d", i))
		}
		a, b := edit(base, "a"), edit(base, "b")
		bb, ab, bbb := lines(base...), lines(a...), lines(b...)
		if got, ok := Merge3(bb, ab, bb); !ok || !bytes.Equal(got, ab) {
			t.Fatalf("iter %d: a vs unchanged base: %q %v", iter, got, ok)
		}
		x, ok1 := Merge3(bb, ab, bbb)
		y, ok2 := Merge3(bb, bbb, ab)
		if ok1 != ok2 || !bytes.Equal(x, y) {
			t.Fatalf("iter %d: asymmetric", iter)
		}
		if !ok1 {
			continue
		}
		for _, side := range [][]string{a, b} {
			for _, l := range side {
				if strings.Contains(l, "-ins-") || strings.Contains(l, "-chg-") {
					if !bytes.Contains(x, []byte(l+"\n")) {
						t.Fatalf("iter %d: clean merge lost %q\nbase %q\na %q\nb %q\ngot %q", iter, l, base, a, b, x)
					}
				}
			}
		}
	}
}

func TestDiffHunksRebuildTheTarget(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	gen := func() []string {
		var out []string
		for i := rng.Intn(40); i > 0; i-- {
			out = append(out, fmt.Sprintf("%d\n", rng.Intn(6)))
		}
		return out
	}
	for iter := 0; iter < 3000; iter++ {
		a, b := gen(), gen()
		hs, ok := diff(a, b)
		if !ok {
			t.Fatal("small diff gave up")
		}
		var got []string
		pos := 0
		for _, h := range hs {
			if h.bs < pos {
				t.Fatalf("iter %d: hunks out of order", iter)
			}
			got = append(got, a[pos:h.bs]...)
			got = append(got, b[h.os:h.oe]...)
			pos = h.be
		}
		got = append(got, a[pos:]...)
		if strings.Join(got, "") != strings.Join(b, "") {
			t.Fatalf("iter %d: rebuilt %q want %q", iter, got, b)
		}
	}
}

func TestHugeRewriteGivesUpQuickly(t *testing.T) {
	var base, a, b []string
	for i := 0; i < 20000; i++ {
		base = append(base, fmt.Sprintf("base %d", i))
		a = append(a, fmt.Sprintf("a %d", i))
		b = append(b, fmt.Sprintf("b %d", i))
	}
	start := time.Now()
	if _, ok := Merge3(lines(base...), lines(a...), lines(b...)); ok {
		t.Fatal("two complete rewrites cannot merge")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %s", d)
	}
}
