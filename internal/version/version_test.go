package version

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		name string
		a, b Vector
		want Order
	}{
		{"both empty", Vector{}, Vector{}, Equal},
		{"nil vs empty", nil, Vector{}, Equal},
		{"equal", Vector{"a": 1, "b": 2}, Vector{"a": 1, "b": 2}, Equal},
		{"before by one", Vector{"a": 1}, Vector{"a": 2}, Before},
		{"after by one", Vector{"a": 3}, Vector{"a": 2}, After},
		{"before with new key", Vector{"a": 1}, Vector{"a": 1, "b": 1}, Before},
		{"after with extra key", Vector{"a": 1, "b": 1}, Vector{"a": 1}, After},
		{"zero entry same as missing", Vector{"a": 1, "b": 0}, Vector{"a": 1}, Equal},
		{"concurrent", Vector{"a": 2, "b": 1}, Vector{"a": 1, "b": 2}, Concurrent},
		{"concurrent disjoint", Vector{"a": 1}, Vector{"b": 1}, Concurrent},
		{"empty before anything", Vector{}, Vector{"a": 1}, Before},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("%s: Compare(%v, %v) = %v, want %v", c.name, c.a, c.b, got, c.want)
		}
	}
}

func TestMergeIsPointwiseMaxAndDominatesBoth(t *testing.T) {
	a := Vector{"a": 3, "b": 1}
	b := Vector{"b": 4, "c": 2}
	m := Merge(a, b)
	want := Vector{"a": 3, "b": 4, "c": 2}
	if Compare(m, want) != Equal {
		t.Fatalf("Merge = %v", m)
	}
	if o := Compare(m, a); o != After {
		t.Errorf("merge vs a = %v", o)
	}
	if o := Compare(m, b); o != After {
		t.Errorf("merge vs b = %v", o)
	}
	// inputs untouched
	if a["b"] != 1 || b["a"] != 0 {
		t.Fatal("Merge mutated its inputs")
	}
}

func TestBumpReturnsNewerCopy(t *testing.T) {
	a := Vector{"x": 1}
	b := a.Bump("x")
	if Compare(b, a) != After || b["x"] != 2 {
		t.Fatalf("Bump = %v", b)
	}
	if a["x"] != 1 {
		t.Fatal("Bump mutated receiver")
	}
	if c := Vector(nil).Bump("y"); c["y"] != 1 {
		t.Fatalf("nil.Bump = %v", c)
	}
}

func TestVersionDeleted(t *testing.T) {
	if !(Version{Hash: Deleted}).IsDeleted() || (Version{Hash: "abc"}).IsDeleted() {
		t.Fatal("IsDeleted wrong")
	}
}
