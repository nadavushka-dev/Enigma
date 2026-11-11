package engine

import (
	"fmt"
	"testing"
)

// settle is a deterministic barrier for "nothing happened" assertions:
// once it returns, everything each machine had to say to the others has
// been received and handled. Each link is one ordered stream and each
// engine handles its messages in order on one loop, so a marker file sent
// after a machine's pending changes arrives after their announcements,
// fetches and files. The markers live in an entry of their own,
// zz-barrier-<name>, so they never show in the entries under test.
func settle(t *testing.T, ms ...*machine) {
	t.Helper()
	for round := 0; round < 2; round++ {
		for _, x := range ms {
			for _, y := range ms {
				if x != y {
					barrier(t, x, y)
				}
			}
		}
	}
}

func barrier(t *testing.T, from, to *machine) {
	t.Helper()
	entry := "zz-barrier-" + from.name
	from.e.Rescan() // flush changes on disk not seen yet
	if from.barrier == 0 {
		from.write(entry+"/0", "0")
		from.add(entry)
	}
	from.barrier++
	rel := fmt.Sprintf("%s/%d", entry, from.barrier)
	content := fmt.Sprint(from.barrier)
	from.write(rel, content)
	from.e.Rescan()
	eventually(t, fmt.Sprintf("barrier %s -> %s", from.name, to.name), hasFile(to, rel, content))
}
