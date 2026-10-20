// Package version implements version vectors and the Version record that
// describes one state of a tracked file (SPEC §6.2).
package version

// Vector maps machine IDs to counters. Missing keys count as zero.
type Vector map[string]uint64

type Order int

const (
	Equal Order = iota
	Before
	After
	Concurrent
)

func (o Order) String() string {
	return [...]string{"equal", "before", "after", "concurrent"}[o]
}

// Compare reports how a relates to b.
func Compare(a, b Vector) Order {
	less, more := false, false
	for k, av := range a {
		if bv := b[k]; av < bv {
			less = true
		} else if av > bv {
			more = true
		}
	}
	for k, bv := range b {
		if _, ok := a[k]; !ok && bv > 0 {
			less = true
		}
	}
	switch {
	case less && more:
		return Concurrent
	case less:
		return Before
	case more:
		return After
	}
	return Equal
}

// Merge returns the pointwise maximum of a and b.
func Merge(a, b Vector) Vector {
	m := make(Vector, len(a)+len(b))
	for k, v := range a {
		m[k] = v
	}
	for k, v := range b {
		if v > m[k] {
			m[k] = v
		}
	}
	return m
}

// Bump returns a copy of v with id's counter incremented.
func (v Vector) Bump(id string) Vector {
	c := Merge(v, nil)
	c[id]++
	return c
}

// Deleted is the Hash of a tombstone version.
const Deleted = "deleted"

// Origin records how a version came to be; it matters only for first-add
// resolution (D7).
type Origin string

const (
	Edit        Origin = ""            // a normal change
	Seed        Origin = "seed"        // made by `enigma add` on the adding machine
	Preexisting Origin = "preexisting" // a file another machine already had when the entry arrived
)

// Version is one state of a tracked file.
type Version struct {
	Hash    string   `json:"hash"` // content SHA-256 or Deleted
	Vector  Vector   `json:"vector"`
	Parents []string `json:"parents,omitempty"`
	Mtime   int64    `json:"mtime"`
	Exec    bool     `json:"exec,omitempty"`
	By      string   `json:"by"`      // machine ID that made it
	ByName  string   `json:"by_name"` // that machine's display name, for conflict copy names
	Origin  Origin   `json:"origin,omitempty"`
}

func (v Version) IsDeleted() bool { return v.Hash == Deleted }
