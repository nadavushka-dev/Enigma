package engine

import (
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSimulationRoots fuzzes what replaces an entry's folder (SPEC §6.5,
// §6.6): random edits and deletes, the root swapped for a symlink to a
// copy, the link removed, the root re-created empty, the copy moved back,
// link cuts and restarts, on three machines. Once every root is a real
// folder again and the machines are quiet:
//   - convergence: identical files everywhere;
//   - no delete nobody made: every path a user wrote and did not delete
//     exists on every machine.
func TestSimulationRoots(t *testing.T) {
	seeds := 8
	if n, err := strconv.Atoi(os.Getenv("ENIGMA_SIM_SEEDS")); err == nil {
		seeds = n
	}
	for i := 0; i < seeds; i++ {
		seed := int64(3000 + i)
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) { simulateRoots(t, seed, 40) })
	}
}

type rootSim struct {
	*sim
	want map[string]bool // paths that must exist everywhere at the end
}

func simulateRoots(t *testing.T, seed int64, steps int) {
	rng := rand.New(rand.NewSource(seed))
	buf := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	// No watcher and no poll: rescans drive everything, and a restore is
	// asked again at most every 200 ms (the poll tick does it in a daemon).
	noWatch := func(o *Options) { o.NoWatch, o.Log, o.RestoreRetry = true, logger, 200*time.Millisecond }
	t.Cleanup(func() {
		if t.Failed() {
			p := filepath.Join(os.TempDir(), fmt.Sprintf("enigma-rootsim-%d.log", seed))
			os.WriteFile(p, buf.Bytes(), 0o644)
			t.Logf("trace: %s", p)
		}
	})
	s := &rootSim{sim: &sim{t: t, trace: buf, cuts: map[[2]int]func(){}, seen: map[string]bool{}, replaced: map[string]bool{}},
		want: map[string]bool{}}
	for _, n := range []string{"a", "b", "c"} {
		s.ms = append(s.ms, newMachine(t, n, noWatch))
	}
	s.userWrite(0, "notes/n1.md", "initial")
	s.want["notes/n1.md"] = true
	s.ms[0].add("notes")
	s.linkAll()
	for _, m := range s.ms {
		eventually(t, m.name+" has n1", hasFile(m, "notes/n1.md", "initial"))
	}
	names := []string{"notes/n1.md", "notes/n2.md", "notes/sub/n3.md"}
	for step := 0; step < steps; step++ {
		m := rng.Intn(len(s.ms))
		switch op := rng.Intn(12); {
		case op < 3:
			name := names[rng.Intn(len(names))]
			if s.rootIsReal(m) {
				s.userWrite(m, name, fmt.Sprintf("%s-%d-%d", s.ms[m].name, seed, step))
				s.want[name] = true
			}
		case op < 4:
			name := names[rng.Intn(len(names))]
			if s.rootIsReal(m) && s.ms[m].read(name) != "<missing>" {
				s.userDelete(m, name)
				delete(s.want, name)
			}
		case op < 8:
			s.rootOp(m, rng.Intn(6))
		case op < 11:
			i, j := rng.Intn(3), rng.Intn(3)
			if i != j {
				s.toggle(i, j)
			}
		default:
			s.restart(m)
		}
		if rng.Intn(3) == 0 {
			time.Sleep(time.Duration(rng.Intn(20)) * time.Millisecond)
		}
	}
	for m := range s.ms { // every root a real folder again
		s.rootOp(m, 3)
		if !s.rootIsReal(m) {
			s.rootOp(m, 2)
		}
	}
	s.linkAll()
	s.converge(seed)
	for _, m := range s.ms {
		for p := range s.want {
			if m.read(p) == "<missing>" {
				s.t.Errorf("seed %d: %s lost %s, which no user deleted", seed, m.name, p)
			}
		}
	}
}

func (s *rootSim) rootIsReal(m int) bool {
	fi, err := os.Lstat(s.ms[m].path("notes"))
	return err == nil && fi.IsDir()
}

// rootOp, on m's loop (one atomic user step):
// 0 swap the root for a symlink to a copy; 1 remove the link (the copy
// stays aside); 2 make the root an empty folder; 3 move the copy back;
// 4 the whole sandbox sequence at once (copy aside, link, unlink, empty
// folder), so the engine never sees the link; 5 the same, and the engine
// is not even told (a missed event: a later rescan or restart finds it).
func (s *rootSim) rootOp(m, op int) {
	mc := s.ms[m]
	root, aside := mc.path("notes"), mc.path("notes.aside")
	mc.e.do(func() {
		fi, err := os.Lstat(root)
		isDir, isLink := err == nil && fi.IsDir(), err == nil && fi.Mode()&os.ModeSymlink != 0
		_, asideErr := os.Lstat(aside)
		switch {
		case op == 0 && isDir && asideErr != nil:
			s.logf("root %s: swap for a link", mc.name)
			os.Rename(root, aside)
			os.Symlink(aside, root)
		case op == 1 && isLink:
			s.logf("root %s: remove the link", mc.name)
			os.Remove(root)
		case op == 2 && err != nil:
			s.logf("root %s: re-create empty", mc.name)
			os.Mkdir(root, 0o755)
		case op == 3 && asideErr == nil && (err != nil || isLink):
			s.logf("root %s: move the copy back", mc.name)
			os.Remove(root)
			os.Rename(aside, root)
		case op >= 4 && isDir && asideErr != nil:
			s.logf("root %s: replaced by an empty folder, unseen=%v", mc.name, op == 5)
			os.Rename(root, aside)
			os.Symlink(aside, root)
			os.Remove(root)
			os.Mkdir(root, 0o755)
			if op == 5 {
				return
			}
		default:
			return
		}
		mc.e.rescanAll()
	})
}

// snapshotNotes is snapshotOf for notes, sorted paths only plus content.
func snapshotNotes(m *machine) string {
	var lines []string
	root := m.path("notes")
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			rel, _ := filepath.Rel(root, p)
			b, _ := os.ReadFile(p)
			lines = append(lines, rel+"="+string(b))
		}
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func (s *rootSim) converge(seed int64) {
	deadline := time.Now().Add(20 * time.Second)
	stable, last := 0, ""
	for stable < 3 {
		if time.Now().After(deadline) {
			for _, m := range s.ms {
				s.t.Logf("%s:\n%s", m.name, snapshotNotes(m))
			}
			s.t.Fatalf("seed %d: machines did not converge", seed)
		}
		for _, m := range s.ms {
			m.e.Rescan()
		}
		time.Sleep(60 * time.Millisecond)
		a := snapshotNotes(s.ms[0])
		same := a == last
		for _, m := range s.ms[1:] {
			if snapshotNotes(m) != a {
				same = false
			}
		}
		last = a
		if same {
			stable++
		} else {
			stable = 0
		}
	}
}
