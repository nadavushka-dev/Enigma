package engine

import (
	"bytes"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSimulation drives random edits, deletes, link cuts and restarts on
// three machines, then checks the invariants of SPEC §13:
//   - convergence: once quiet, every machine has identical files;
//   - no silent loss: every line Enigma saw and nobody knowingly replaced
//     still exists somewhere (a file on any machine, a trash or a backup).
//     Lines, not whole contents, so a clean merge counts as keeping both
//     edits (SPEC §7 step 3).
//
// ENIGMA_SIM_SEEDS raises the number of seeds (CI). A failing seed is
// printed; pin it in regressionSeeds so it is checked forever.
func TestSimulation(t *testing.T) {
	seeds := 12
	if n, err := strconv.Atoi(os.Getenv("ENIGMA_SIM_SEEDS")); err == nil {
		seeds = n
	}
	regressionSeeds := []int64{1, 7, 1009, 1018, 1043} // seeds that once failed; keep forever
	all := append([]int64{}, regressionSeeds...)
	for i := 0; i < seeds; i++ {
		all = append(all, int64(1000+i))
	}
	for _, seed := range all {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) { simulate(t, seed, 40) })
	}
}

type sim struct {
	t        *testing.T
	trace    *syncBuffer
	ms       []*machine
	cuts     map[[2]int]func()
	seen     map[string]bool // lines Enigma has seen
	replaced map[string]bool // lines a user knowingly overwrote or deleted
}

func simulate(t *testing.T, seed int64, steps int) {
	rng := rand.New(rand.NewSource(seed))
	// Every run is traced into memory; a failing seed's trace is written to
	// a file, since timing-dependent failures may not reproduce on demand.
	buf := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	noWatch := func(o *Options) {
		o.NoWatch = true
		o.Log = logger
	}
	t.Cleanup(func() {
		if t.Failed() {
			p := filepath.Join(os.TempDir(), fmt.Sprintf("enigma-sim-%d.log", seed))
			os.WriteFile(p, buf.Bytes(), 0o644)
			t.Logf("trace: %s", p)
		}
	})
	s := &sim{t: t, trace: buf, cuts: map[[2]int]func(){}, seen: map[string]bool{}, replaced: map[string]bool{}}
	for _, n := range []string{"a", "b", "c"} {
		s.ms = append(s.ms, newMachine(t, n, noWatch))
	}
	names := []string{"sim/one.md", "sim/two.md", "sim/deep/three.md", "sim/four.txt"}
	const notes = "sim/notes.md"
	s.userWrite(0, names[0], "initial")
	s.userWrite(0, notes, "n0\nn1\nn2\nn3\nn4\nn5\n")
	s.ms[0].add("sim")
	s.linkAll()
	// Line edits of a multi-line file make mergeable conflicts. They draw
	// from their own stream so pinned seeds keep their other operations.
	lineRng := rand.New(rand.NewSource(seed ^ 0x5eed))

	for step := 0; step < steps; step++ {
		m := rng.Intn(len(s.ms))
		switch op := rng.Intn(10); {
		case op < 5:
			s.userWrite(m, names[rng.Intn(len(names))], fmt.Sprintf("%s-%d-%d", s.ms[m].name, seed, step))
		case op < 7:
			s.userDelete(m, names[rng.Intn(len(names))])
		case op < 9:
			i, j := rng.Intn(3), rng.Intn(3)
			if i != j {
				s.toggle(i, j)
			}
		default:
			s.restart(m)
		}
		if lineRng.Intn(3) == 0 {
			m := lineRng.Intn(len(s.ms))
			s.userEditLine(m, notes, lineRng.Intn(6), fmt.Sprintf("%s-line-%d-%d", s.ms[m].name, seed, step))
		}
		if rng.Intn(3) == 0 {
			time.Sleep(time.Duration(rng.Intn(20)) * time.Millisecond)
		}
	}

	s.linkAll()
	s.converge(seed)
	s.checkNoLoss(seed)
}

func (s *sim) logf(format string, args ...any) {
	fmt.Fprintf(s.trace, "SIM "+format+"\n", args...)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.b.Bytes()...)
}

// userWrite and userDelete run on the machine's engine loop, so the read
// of what is being replaced and the write itself are one atomic step with
// respect to the engine. Otherwise the oracle can blame Enigma for content
// the simulated user overwrote (a stale-editor save, which no sync tool can
// detect). Races inside one machine are covered by dedicated tests instead
// (fsx write races, TestUserEditBetweenDecideAndApplyIsKept).
func (s *sim) userWrite(m int, rel, content string) {
	mc := s.ms[m]
	mc.e.do(func() {
		old := mc.read(rel)
		s.logf("write %s %s = %q (was %q)", mc.name, rel, content, old)
		if old != "<missing>" {
			s.mark(s.replaced, old)
		}
		p := mc.path(rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
		mc.e.rescanAll()
		s.mark(s.seen, content)
	})
}

func (s *sim) userDelete(m int, rel string) {
	mc := s.ms[m]
	mc.e.do(func() {
		old := mc.read(rel)
		if old == "<missing>" {
			return
		}
		s.logf("delete %s %s (was %q)", mc.name, rel, old)
		s.mark(s.replaced, old)
		os.Remove(mc.path(rel))
		mc.e.rescanAll()
	})
}

// userEditLine replaces line i of rel, as an editor would.
func (s *sim) userEditLine(m int, rel string, i int, line string) {
	mc := s.ms[m]
	mc.e.do(func() {
		old := mc.read(rel)
		if old == "<missing>" {
			return
		}
		ls := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
		if i >= len(ls) {
			ls = append(ls, line)
		} else {
			s.replaced[ls[i]] = true
			ls[i] = line
		}
		s.logf("edit line %s %s:%d = %q", mc.name, rel, i, line)
		os.WriteFile(mc.path(rel), []byte(strings.Join(ls, "\n")+"\n"), 0o644)
		mc.e.rescanAll()
		s.seen[line] = true
	})
}

func (s *sim) mark(set map[string]bool, content string) {
	for _, l := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
		set[l] = true
	}
}

func (s *sim) toggle(i, j int) {
	if i > j {
		i, j = j, i
	}
	k := [2]int{i, j}
	if cut, ok := s.cuts[k]; ok {
		s.logf("cut %s-%s", s.ms[i].name, s.ms[j].name)
		cut()
		delete(s.cuts, k)
		return
	}
	s.logf("link %s-%s", s.ms[i].name, s.ms[j].name)
	s.cuts[k] = link(s.t, s.ms[i], s.ms[j])
}

func (s *sim) restart(m int) {
	s.logf("restart %s", s.ms[m].name)
	for k, cut := range s.cuts {
		if k[0] == m || k[1] == m {
			cut()
			delete(s.cuts, k)
		}
	}
	s.ms[m].restart()
}

func (s *sim) linkAll() {
	for i := 0; i < len(s.ms); i++ {
		for j := i + 1; j < len(s.ms); j++ {
			if _, ok := s.cuts[[2]int{i, j}]; !ok {
				s.cuts[[2]int{i, j}] = link(s.t, s.ms[i], s.ms[j])
			}
		}
	}
}

// snapshot lists every file under ~/sim as "path=content".
func snapshot(m *machine) string {
	var lines []string
	root := m.path("sim")
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

func (s *sim) converge(seed int64) {
	deadline := time.Now().Add(15 * time.Second)
	stable := 0
	var last string
	for stable < 3 {
		if time.Now().After(deadline) {
			for _, m := range s.ms {
				s.t.Logf("%s:\n%s", m.name, snapshot(m))
			}
			s.t.Fatalf("seed %d: machines did not converge", seed)
		}
		for _, m := range s.ms {
			m.e.Rescan()
		}
		time.Sleep(60 * time.Millisecond)
		a := snapshot(s.ms[0])
		same := a == last
		for _, m := range s.ms[1:] {
			if snapshot(m) != a {
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

func (s *sim) checkNoLoss(seed int64) {
	have := map[string]bool{}
	for _, m := range s.ms {
		for _, dir := range []string{m.path("sim"), filepath.Join(m.data, "trash"), filepath.Join(m.data, "backups")} {
			filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
				if err == nil && !fi.IsDir() {
					b, _ := os.ReadFile(p)
					s.mark(have, string(b))
				}
				return nil
			})
		}
	}
	for c := range s.seen {
		if !s.replaced[c] && !have[c] {
			s.t.Errorf("seed %d: line %q was lost", seed, c)
		}
	}
}
