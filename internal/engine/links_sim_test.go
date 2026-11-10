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

// TestSimulationLinks drives random link creates, retargets and deletes,
// files replacing links (and back), file edits, link cuts and restarts on
// three machines in an entry with links on (D23), then checks:
//   - convergence: once quiet, every machine has the same links (by stored
//     target) and the same files;
//   - never through a link: what the links point at is never changed;
//   - no silent loss: every file line seen and not knowingly replaced still
//     exists somewhere, and every link target a user set and did not
//     knowingly replace is a link on some machine or in some trash.
//
// Targets never go away here, so no link waits for long; pending and
// dangling links have their own tests.
func TestSimulationLinks(t *testing.T) {
	seeds := 8
	if n, err := strconv.Atoi(os.Getenv("ENIGMA_SIM_SEEDS")); err == nil {
		seeds = n
	}
	for i := 0; i < seeds; i++ {
		seed := int64(2000 + i)
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) { simulateLinks(t, seed, 40) })
	}
}

type linkSim struct {
	*sim
	linksSeen, linksReplaced map[string]bool // stored targets
}

var simTargets = []string{"d1", "d2", "d3"}

func simulateLinks(t *testing.T, seed int64, steps int) {
	rng := rand.New(rand.NewSource(seed))
	buf := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	noWatch := func(o *Options) { o.NoWatch, o.Log = true, logger }
	t.Cleanup(func() {
		if t.Failed() {
			p := filepath.Join(os.TempDir(), fmt.Sprintf("enigma-linksim-%d.log", seed))
			os.WriteFile(p, buf.Bytes(), 0o644)
			t.Logf("trace: %s", p)
		}
	})
	s := &linkSim{
		sim:       &sim{t: t, trace: buf, cuts: map[[2]int]func(){}, seen: map[string]bool{}, replaced: map[string]bool{}},
		linksSeen: map[string]bool{}, linksReplaced: map[string]bool{},
	}
	for _, n := range []string{"a", "b", "c"} {
		s.ms = append(s.ms, newMachine(t, n, noWatch))
	}
	a := s.ms[0]
	for _, d := range simTargets {
		a.write("tgt/"+d+"/SKILL.md", "skill "+d)
	}
	a.write("lnk/readme.md", "r0")
	a.add("tgt")
	a.addLinks("lnk")
	s.linkAll()
	for _, m := range s.ms {
		eventually(t, m.name+" has the targets", hasFile(m, "tgt/d3/SKILL.md", "skill d3"))
	}

	names := []string{"lnk/x", "lnk/y", "lnk/z"}
	for step := 0; step < steps; step++ {
		m := rng.Intn(len(s.ms))
		name := names[rng.Intn(len(names))]
		switch op := rng.Intn(12); {
		case op < 4:
			s.userLink(m, name, simTargets[rng.Intn(len(simTargets))], rng.Intn(2) == 0)
		case op < 5:
			s.userUnlink(m, name)
		case op < 7:
			s.userFile(m, name, fmt.Sprintf("%s-file-%d-%d", s.ms[m].name, seed, step))
		case op < 8:
			s.userWrite(m, "lnk/readme.md", fmt.Sprintf("%s-readme-%d-%d", s.ms[m].name, seed, step))
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
	s.linkAll()
	s.convergeLinks(seed)
	s.checkTargetsUntouched(seed)
	s.checkNoLinkLoss(seed)
}

// storedOf is the stored form of the link at rel on m, or "" for none.
func storedOf(m *machine, rel string) string {
	raw, err := os.Readlink(m.path(rel))
	if err != nil {
		return ""
	}
	stored, _, err := storeLinkTarget([]string{m.home}, rel, raw)
	if err != nil {
		return "invalid:" + raw
	}
	return stored
}

// userLink makes rel a link to tgt/<d>, replacing what is there (ln -sfn),
// absolute under the machine's home or relative.
func (s *linkSim) userLink(m int, rel, d string, absolute bool) {
	mc := s.ms[m]
	mc.e.do(func() {
		target := "../tgt/" + d
		if absolute {
			target = mc.path("tgt/" + d)
		}
		s.replaceAt(mc, rel)
		s.logf("link %s %s -> %s", mc.name, rel, target)
		tmp := mc.path(rel) + ".simtmp"
		os.Remove(tmp)
		if err := os.Symlink(target, tmp); err != nil {
			s.t.Fatal(err)
		}
		os.Rename(tmp, mc.path(rel))
		s.linksSeen[storedOf(mc, rel)] = true
		mc.e.rescanAll()
	})
}

func (s *linkSim) userUnlink(m int, rel string) {
	mc := s.ms[m]
	mc.e.do(func() {
		if storedOf(mc, rel) == "" {
			return
		}
		s.logf("unlink %s %s", mc.name, rel)
		s.replaceAt(mc, rel)
		os.Remove(mc.path(rel))
		mc.e.rescanAll()
	})
}

// userFile replaces whatever is at rel with a regular file.
func (s *linkSim) userFile(m int, rel, content string) {
	mc := s.ms[m]
	mc.e.do(func() {
		s.replaceAt(mc, rel)
		s.logf("file %s %s = %q", mc.name, rel, content)
		os.Remove(mc.path(rel))
		os.WriteFile(mc.path(rel), []byte(content), 0o644)
		s.mark(s.seen, content)
		mc.e.rescanAll()
	})
}

// replaceAt records that the user knowingly replaces what is at rel.
func (s *linkSim) replaceAt(mc *machine, rel string) {
	if st := storedOf(mc, rel); st != "" {
		s.linksReplaced[st] = true
		return
	}
	if old := mc.read(rel); old != "<missing>" {
		s.mark(s.replaced, old)
	}
}

// linkSnapshot lists ~/lnk without following links: "path=content" for a
// file, "path->stored" for a link.
func linkSnapshot(m *machine) string {
	var lines []string
	root := m.path("lnk")
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(m.home, p)
		rel = filepath.ToSlash(rel)
		if fi.Mode()&os.ModeSymlink != 0 {
			lines = append(lines, rel+"->"+storedOf(m, rel))
			return nil
		}
		b, _ := os.ReadFile(p)
		lines = append(lines, rel+"="+string(b))
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func (s *linkSim) convergeLinks(seed int64) {
	deadline := time.Now().Add(15 * time.Second)
	stable := 0
	last := ""
	for stable < 3 {
		if time.Now().After(deadline) {
			for _, m := range s.ms {
				s.t.Logf("%s:\n%s", m.name, linkSnapshot(m))
			}
			s.t.Fatalf("seed %d: machines did not converge", seed)
		}
		for _, m := range s.ms {
			m.e.Rescan()
		}
		time.Sleep(60 * time.Millisecond)
		a := linkSnapshot(s.ms[0])
		same := a == last
		for _, m := range s.ms[1:] {
			if linkSnapshot(m) != a {
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

// checkTargetsUntouched: nothing was written, copied or trashed through a
// link: every target folder still holds exactly its SKILL.md.
func (s *linkSim) checkTargetsUntouched(seed int64) {
	for _, m := range s.ms {
		for _, d := range simTargets {
			ents, _ := os.ReadDir(m.path("tgt/" + d))
			if len(ents) != 1 || ents[0].Name() != "SKILL.md" || m.read("tgt/"+d+"/SKILL.md") != "skill "+d {
				names := []string{}
				for _, e := range ents {
					names = append(names, e.Name())
				}
				s.t.Errorf("seed %d: %s: tgt/%s changed through a link: %v", seed, m.name, d, names)
			}
		}
	}
}

func (s *linkSim) checkNoLinkLoss(seed int64) {
	have, links := map[string]bool{}, map[string]bool{}
	for _, m := range s.ms {
		for _, dir := range []string{m.path("lnk"), filepath.Join(m.data, "trash"), filepath.Join(m.data, "backups")} {
			filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
				switch {
				case err != nil || fi.IsDir():
				case fi.Mode()&os.ModeSymlink != 0:
					raw, _ := os.Readlink(p)
					if stored, _, err := storeLinkTarget([]string{m.home}, "lnk/x", raw); err == nil {
						links[stored] = true
					}
				default:
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
	for l := range s.linksSeen {
		if !s.linksReplaced[l] && !links[l] {
			s.t.Errorf("seed %d: link target %q was lost", seed, l)
		}
	}
}
