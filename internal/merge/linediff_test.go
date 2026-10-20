package merge

import (
	"reflect"
	"strings"
	"testing"
)

func TestLineChanges(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		wa   []bool
		wb   []bool
	}{
		{"identical", "a\nb\nc\n", "a\nb\nc\n", []bool{false, false, false}, []bool{false, false, false}},
		{"one changed", "a\nb\nc\n", "a\nX\nc\n", []bool{false, true, false}, []bool{false, true, false}},
		{"insertion", "a\nc\n", "a\nb\nc\n", []bool{false, false}, []bool{false, true, false}},
		{"deletion", "a\nb\nc\n", "a\nc\n", []bool{false, true, false}, []bool{false, false}},
		{"both empty", "", "", nil, nil},
		{"a empty", "", "x\ny\n", nil, []bool{true, true}},
		{"b empty", "x\n", "", []bool{true}, nil},
		{"no trailing newline", "a\nb", "a\nb\n", []bool{false, true}, []bool{false, true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ga, gb := LineChanges([]byte(c.a), []byte(c.b))
			if !reflect.DeepEqual(ga, c.wa) || !reflect.DeepEqual(gb, c.wb) {
				t.Fatalf("got a=%v b=%v, want a=%v b=%v", ga, gb, c.wa, c.wb)
			}
		})
	}
}

func TestLineChangesTooDifferentMarksEverything(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < maxEdits+5; i++ {
		a.WriteString("a\n")
		b.WriteString("b\n")
	}
	ga, gb := LineChanges([]byte(a.String()), []byte(b.String()))
	if len(ga) != maxEdits+5 || len(gb) != maxEdits+5 {
		t.Fatalf("lengths %d %d", len(ga), len(gb))
	}
	for i := range ga {
		if !ga[i] || !gb[i] {
			t.Fatalf("line %d not marked", i)
		}
	}
}
