package version

import (
	"reflect"
	"testing"
)

func TestWalkIsBreadthFirstFromStartAndBounded(t *testing.T) {
	g := Graph{"m": {"a", "b"}, "a": {"base"}, "b": {"base"}, "base": {"root"}}
	got := Walk(g, "m", 10)
	want := []Node{{"m", []string{"a", "b"}}, {"a", []string{"base"}}, {"b", []string{"base"}}, {"base", []string{"root"}}, {"root", nil}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if got := Walk(g, "m", 2); len(got) != 2 {
		t.Fatalf("limit not applied: %v", got)
	}
}

func TestWalkSurvivesCycles(t *testing.T) {
	// A revert brings a hash back, so the hash graph can loop.
	g := Graph{"x": {"y"}, "y": {"x"}}
	if got := Walk(g, "x", 10); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestWalkUnknownStartIsJustItself(t *testing.T) {
	if got := Walk(Graph{}, "s", 10); !reflect.DeepEqual(got, []Node{{Hash: "s"}}) {
		t.Fatalf("got %v", got)
	}
}

func TestCommonBasePicksClosestToBoth(t *testing.T) {
	// root - base - a1 - a2   (one side)
	//            \- b1        (other side)
	a := Walk(Graph{"a2": {"a1"}, "a1": {"base"}, "base": {"root"}}, "a2", 64)
	b := Walk(Graph{"b1": {"base"}, "base": {"root"}}, "b1", 64)
	if got, ok := CommonBase(a, b); !ok || got != "base" {
		t.Fatalf("got %q %v", got, ok)
	}
}

func TestCommonBaseIsSymmetric(t *testing.T) {
	// Two candidate bases at different distances: the result must not
	// depend on which side is "ours" (SPEC §7 determinism).
	a := Walk(Graph{"a": {"p", "q"}, "p": {"q"}}, "a", 64)
	b := Walk(Graph{"b": {"q"}, "q": {"p"}}, "b", 64)
	x, ok1 := CommonBase(a, b)
	y, ok2 := CommonBase(b, a)
	if !ok1 || !ok2 || x != y {
		t.Fatalf("asymmetric: %q vs %q", x, y)
	}
}

func TestCommonBaseTieBreaksOnLowestHash(t *testing.T) {
	a := Walk(Graph{"a": {"z", "k"}}, "a", 64)
	b := Walk(Graph{"b": {"z", "k"}}, "b", 64)
	if got, _ := CommonBase(a, b); got != "k" {
		t.Fatalf("got %q", got)
	}
}

func TestCommonBaseIgnoresTombstones(t *testing.T) {
	a := Walk(Graph{"a": {Deleted}}, "a", 64)
	b := Walk(Graph{"b": {Deleted}}, "b", 64)
	if got, ok := CommonBase(a, b); ok {
		t.Fatalf("a delete is no merge base, got %q", got)
	}
}

func TestCommonBaseNone(t *testing.T) {
	if _, ok := CommonBase(Walk(Graph{}, "a", 64), Walk(Graph{}, "b", 64)); ok {
		t.Fatal("unrelated versions have no base")
	}
}

func TestGraphAddKeepsParentsUnion(t *testing.T) {
	g := Graph{}
	g.Add([]Node{{"x", []string{"p"}}})
	g.Add([]Node{{"x", []string{"q", "p"}}, {"p", nil}})
	if !reflect.DeepEqual(g["x"], []string{"p", "q"}) {
		t.Fatalf("got %v", g["x"])
	}
}
