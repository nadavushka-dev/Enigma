package engine

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nadavushka-dev/enigma/internal/proto"
)

// The shapes found on the user's fleet in ~/.agents (one folder entry),
// checked with links off (the default after an update) and on.

var tktSkills = []string{"tkt-work", "tkt-ship", "tkt-context", "tkt-evidence", "tkt-squad-loop", "autonomous-squad"}

const pluginSkills = ".claude/plugins/marketplaces/nadavushka-plugs/squad/skills"

// knownFleet links ms pairwise and waits until every machine knows every
// other (as after `enigma machine add`).
func knownFleet(t *testing.T, ms ...*machine) {
	t.Helper()
	for i := range ms {
		for j := i + 1; j < len(ms); j++ {
			link(t, ms[i], ms[j])
		}
	}
	for _, m := range ms {
		eventually(t, m.name+" knows everyone", func() bool { return len(m.e.Machines()) == len(ms) })
	}
}

func addAgents(t *testing.T, m *machine, links bool) {
	t.Helper()
	if err := m.e.AddEntry(m.path(".agents"), AddOptions{Links: links}); err != nil {
		t.Fatal(err)
	}
}

// versionsUnder is m's current versions under dir, printed: they change
// only when some machine makes a version there.
func versionsUnder(m *machine, dir string) string {
	var out []string
	m.e.do(func() {
		for rel, fs := range m.e.st.Files {
			if strings.HasPrefix(rel, dir+"/") {
				out = append(out, rel+"="+fmtVersion(&fs.V))
			}
		}
	})
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func skippedHas(m *machine, rel string) bool {
	for _, es := range m.e.Status().Entries {
		if contains(es.Skipped, rel) {
			return true
		}
	}
	return false
}

func eachLinksMode(t *testing.T, f func(t *testing.T, links bool)) {
	for _, links := range []bool{false, true} {
		name := "links-off"
		if links {
			name = "links-on"
		}
		t.Run(name, func(t *testing.T) { f(t, links) })
	}
}

// Shape 1: per-skill links to a target outside every entry (a plugin
// folder), identical on every machine. Skipped and reported, never
// replicated, never a delete, the targets untouched, no churn.
func TestRealFleet_PerSkillLinksToAnUnsyncedTarget(t *testing.T) {
	eachLinksMode(t, func(t *testing.T, links bool) {
		ms := []*machine{newMachine(t, "u1"), newMachine(t, "u2"), newMachine(t, "u3")}
		for _, m := range ms {
			m.write(".agents/skills/real/SKILL.md", "real")
			for _, s := range tktSkills {
				m.write(pluginSkills+"/"+s+"/SKILL.md", "plugin "+s)
				m.symlink(".agents/skills/"+s, m.path(pluginSkills+"/"+s))
			}
		}
		knownFleet(t, ms...)
		addAgents(t, ms[0], links)
		for _, m := range ms[1:] {
			eventually(t, m.name+" has the entry", func() bool { return hasEntry(m, ".agents") })
		}
		settle(t, ms...)
		before := map[string]string{}
		for _, m := range ms {
			before[m.name] = versionsUnder(m, ".agents")
		}
		for _, m := range ms {
			m.e.Rescan()
		}
		settle(t, ms...)
		for _, m := range ms {
			if versionsUnder(m, ".agents") != before[m.name] {
				t.Fatalf("%s made versions on a quiet fleet (churn)", m.name)
			}
			for _, s := range tktSkills {
				rel := ".agents/skills/" + s
				if cur(m, rel) != nil || cur(m, rel+"/SKILL.md") != nil {
					t.Fatalf("%s tracks %s", m.name, rel)
				}
				if m.readlink(rel) != m.path(pluginSkills+"/"+s) {
					t.Fatalf("%s: link %s changed", m.name, rel)
				}
				if got := listDir(t, m.path(pluginSkills+"/"+s)); len(got) != 1 || m.read(pluginSkills+"/"+s+"/SKILL.md") != "plugin "+s {
					t.Fatalf("%s: plugin %s touched: %v", m.name, s, got)
				}
				if !skippedHas(m, rel) {
					t.Fatalf("%s does not report %s as skipped", m.name, rel)
				}
				if links {
					if l, ok := linkStatus(m, rel); !ok || l.State != LinkSkipped || !strings.Contains(l.Reason, "not synced") {
						t.Fatalf("%s: %s status %+v", m.name, rel, l)
					}
				}
			}
			if m.read(".agents/skills/real/SKILL.md") != "real" || trashed(m, "real") {
				t.Fatalf("%s lost the real skill", m.name)
			}
		}
	})
}

// Shape 2: stray relative links inside a real skill folder, on one
// machine, resolving to ~/.agents/.agents/skills/… (dangling). Skipped
// and reported, never replicated or followed; the sibling files sync.
func TestRealFleet_StrayRelativeLinksInsideASkill(t *testing.T) {
	eachLinksMode(t, func(t *testing.T, links bool) {
		u1, u2 := newMachine(t, "u1"), newMachine(t, "u2")
		for _, s := range []string{"brain-frontman", "squad-supervisor"} {
			u1.write(".agents/skills/"+s+"/SKILL.md", s+" v1")
			u1.symlink(".agents/skills/"+s+"/"+s, "../../.agents/skills/"+s)
		}
		knownFleet(t, u1, u2)
		addAgents(t, u1, links)
		eventually(t, "sibling synced", hasFile(u2, ".agents/skills/brain-frontman/SKILL.md", "brain-frontman v1"))
		u1.write(".agents/skills/squad-supervisor/SKILL.md", "squad-supervisor v2")
		u1.e.Rescan()
		eventually(t, "sibling edit synced", hasFile(u2, ".agents/skills/squad-supervisor/SKILL.md", "squad-supervisor v2"))
		settle(t, u1, u2)
		for _, s := range []string{"brain-frontman", "squad-supervisor"} {
			rel := ".agents/skills/" + s + "/" + s
			if u2.readlink(rel) != "<no link>" || cur(u2, rel) != nil || cur(u1, rel) != nil {
				t.Fatalf("%s was replicated", rel)
			}
			if u1.readlink(rel) != "../../.agents/skills/"+s {
				t.Fatalf("u1's own %s changed", rel)
			}
			if !skippedHas(u1, rel) {
				t.Fatalf("%s not reported", rel)
			}
			if links {
				if l, _ := linkStatus(u1, rel); l.State != LinkSkipped || !strings.Contains(l.Reason, "not here") {
					t.Fatalf("%s status %+v", rel, l)
				}
			}
		}
		if _, err := os.Lstat(u1.path(".agents/.agents")); err == nil {
			t.Fatal("something was created where the stray links point")
		}
	})
}

// joinMac starts the newcomer "mac" (records as `enigma join` hands them
// over), lets prep lay out its disk, and links it to the members.
func joinMac(t *testing.T, members []*machine, prep func(mac *machine)) *machine {
	t.Helper()
	boot := append(members[0].e.Machines(), proto.MachineRecord{ID: "id-mac", Name: "mac", Clock: 1, By: "id-mac"})
	mac := newMachine(t, "mac", func(o *Options) { o.Bootstrap = boot })
	prep(mac)
	for _, m := range members {
		link(t, m, mac)
	}
	eventually(t, "mac has the entry", func() bool { return hasEntry(mac, ".agents") })
	return mac
}

// holds reports whether content is somewhere m keeps it for rel: the file,
// a conflict copy next to it, or a first-add backup.
func holds(m *machine, rel, content string) bool {
	if m.read(rel) == content || backedUp(m, content) {
		return true
	}
	dir, base := filepath.Split(rel)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	for _, c := range conflictCopies(m, strings.TrimSuffix(dir, "/"), stem) {
		if c == content {
			return true
		}
	}
	return false
}

// Shape 3, the other layout: the newcomer's ~/.agents/skills is itself a
// symlink into its repo, where members have a real folder. Nothing on the
// members, and nothing behind the link, is deleted or overwritten; when
// the newcomer makes it a real folder, the members' skills come back.
func TestRealFleet_MacWhoseSkillsFolderIsALink(t *testing.T) {
	for _, mode := range []string{"links-off", "links-on", "links-on-repo-synced"} {
		t.Run(mode, func(t *testing.T) {
			u1, u2 := newMachine(t, "u1"), newMachine(t, "u2")
			for _, m := range []*machine{u1, u2} {
				m.write(".agents/skills/tkt-work/SKILL.md", "ubuntu tkt-work")
				m.write(".agents/skills/other/SKILL.md", "other")
				m.write(".agents/AGENTS.md", "agents")
			}
			knownFleet(t, u1, u2)
			addAgents(t, u1, mode != "links-off")
			eventually(t, "u2 has the entry", func() bool { return hasEntry(u2, ".agents") })
			settle(t, u1, u2)
			const repo = "work/nadavushka-plugs/squad/skills"
			mac := joinMac(t, []*machine{u1, u2}, func(mac *machine) {
				mac.write(repo+"/tkt-work/SKILL.md", "mac repo tkt-work")
				mac.write(repo+"/tkt-ship/SKILL.md", "mac repo tkt-ship")
				mac.symlink(".agents/skills", mac.path(repo))
			})
			eventually(t, "mac has AGENTS.md", hasFile(mac, ".agents/AGENTS.md", "agents"))
			if mode == "links-on-repo-synced" {
				if err := mac.e.AddEntry(mac.path(repo), AddOptions{}); err != nil {
					t.Fatal(err)
				}
				eventually(t, "u1 has the repo", hasFile(u1, repo+"/tkt-ship/SKILL.md", "mac repo tkt-ship"))
				eventually(t, "u1 reports mac's link blocked by its folder", linkState(u1, ".agents/skills", LinkBlocked))
			}
			settle(t, u1, u2, mac)
			for _, m := range []*machine{u1, u2} {
				if m.read(".agents/skills/tkt-work/SKILL.md") != "ubuntu tkt-work" || m.read(".agents/skills/other/SKILL.md") != "other" ||
					isSymlink(m.path(".agents/skills")) || trashed(m, "ubuntu tkt-work") || trashed(m, "other") {
					t.Fatalf("%s's real skills changed", m.name)
				}
			}
			if got := listDir(t, mac.path(repo)); len(got) != 2 || mac.read(repo+"/tkt-work/SKILL.md") != "mac repo tkt-work" {
				t.Fatalf("written through mac's skills link: %v", got)
			}
			if !isSymlink(mac.path(".agents/skills")) {
				t.Fatal("mac's skills link was replaced")
			}

			os.Remove(mac.path(".agents/skills"))
			mac.e.Rescan()
			eventually(t, "mac gets the members' skills", hasFile(mac, ".agents/skills/other/SKILL.md", "other"))
			eventually(t, "mac gets tkt-work", hasFile(mac, ".agents/skills/tkt-work/SKILL.md", "ubuntu tkt-work"))
			settle(t, u1, u2, mac)
			for _, m := range []*machine{u1, u2} {
				if m.read(".agents/skills/tkt-work/SKILL.md") != "ubuntu tkt-work" || m.read(".agents/skills/other/SKILL.md") != "other" {
					t.Fatalf("%s lost skills when mac's link went", m.name)
				}
			}
			if mac.read(repo+"/tkt-work/SKILL.md") != "mac repo tkt-work" {
				t.Fatal("mac's repo copy changed")
			}
		})
	}
}

// D22 when the newcomer has its own, differing files at the members'
// paths (and one only it has): nothing is lost on either side. Keep-both
// keeps its copy as a conflict copy, this-machine-wins backs it up, and a
// later edit on a member makes an ordinary conflict (§7 step 4).
func TestRealFleet_MacJoinsWithDifferingFiles(t *testing.T) {
	for _, mode := range []string{"keep-both", "mine-wins", "edited-after-add"} {
		t.Run(mode, func(t *testing.T) {
			u1, u2 := newMachine(t, "u1"), newMachine(t, "u2")
			u1.write(".agents/skills/other/SKILL.md", "members' other")
			knownFleet(t, u1, u2)
			policy := proto.FirstAddKeepBoth
			if mode == "mine-wins" {
				policy = proto.FirstAddReplace
			}
			if err := u1.e.AddEntry(u1.path(".agents"), AddOptions{Policy: policy}); err != nil {
				t.Fatal(err)
			}
			eventually(t, "u2 has it", hasFile(u2, ".agents/skills/other/SKILL.md", "members' other"))
			if mode == "edited-after-add" {
				u1.write(".agents/skills/other/SKILL.md", "members' edit")
				u1.e.Rescan()
				eventually(t, "u2 has the edit", hasFile(u2, ".agents/skills/other/SKILL.md", "members' edit"))
			}
			settle(t, u1, u2)
			members := "members' other"
			if mode == "edited-after-add" {
				members = "members' edit"
			}
			mac := joinMac(t, []*machine{u1, u2}, func(mac *machine) {
				mac.write(".agents/skills/other/SKILL.md", "mac's own other")
				mac.write(".agents/skills/maconly/SKILL.md", "mac only")
			})
			eventually(t, "mac's own skill spreads (D14)", hasFile(u1, ".agents/skills/maconly/SKILL.md", "mac only"))
			settle(t, u1, u2, mac)
			for _, m := range []*machine{u1, u2, mac} {
				if !holds(m, ".agents/skills/other/SKILL.md", members) {
					t.Fatalf("the members' version is lost (%s)", m.name)
				}
			}
			if !holds(mac, ".agents/skills/other/SKILL.md", "mac's own other") {
				t.Fatal("mac's own version is lost")
			}
			switch mode {
			case "keep-both":
				if mac.read(".agents/skills/other/SKILL.md") != members || !holds(u1, ".agents/skills/other/SKILL.md", "mac's own other") {
					t.Fatal("keep-both: the members' version must win in place and mac's travel as a copy")
				}
			case "mine-wins":
				if mac.read(".agents/skills/other/SKILL.md") != members || !backedUp(mac, "mac's own other") {
					t.Fatal("mine-wins: mac's copy must be backed up and replaced")
				}
			case "edited-after-add":
				if u1.read(".agents/skills/other/SKILL.md") != mac.read(".agents/skills/other/SKILL.md") {
					t.Fatal("the machines did not converge")
				}
			}
		})
	}
}

// Shape 3: a machine joins whose ~/.agents/skills holds a symlinked
// folder (skills -> a repo's skills) and per-skill links into that repo,
// where the members have real folders. Nothing on the members, and
// nothing behind the newcomer's links, is deleted or overwritten. Then
// the newcomer removes one link: the members' files come back into the
// now-real folder (restore, never delete).
func TestRealFleet_MacJoinsWithLinkedSkillFolders(t *testing.T) {
	for _, mode := range []string{"links-off", "links-on", "links-on-repo-synced"} {
		t.Run(mode, func(t *testing.T) {
			links := mode != "links-off"
			u1, u2 := newMachine(t, "u1"), newMachine(t, "u2")
			for _, m := range []*machine{u1, u2} {
				m.write(".agents/skills/tkt-work/SKILL.md", "ubuntu tkt-work")
				m.write(".agents/skills/other/SKILL.md", "other")
			}
			knownFleet(t, u1, u2)
			addAgents(t, u1, links)
			eventually(t, "u2 has the entry", func() bool { return hasEntry(u2, ".agents") })
			settle(t, u1, u2)

			const repo = "work/nadavushka-plugs/squad/skills"
			boot := append(u1.e.Machines(), proto.MachineRecord{ID: "id-mac", Name: "mac", Clock: 1, By: "id-mac"})
			mac := newMachine(t, "mac", func(o *Options) { o.Bootstrap = boot })
			mac.write(repo+"/tkt-work/SKILL.md", "mac repo tkt-work")
			mac.write(repo+"/tkt-ship/SKILL.md", "mac repo tkt-ship")
			mac.symlink(".agents/skills/skills", mac.path(repo))
			mac.symlink(".agents/skills/tkt-work", mac.path(repo+"/tkt-work"))
			link(t, u1, mac)
			link(t, u2, mac)
			eventually(t, "mac has the real skill", hasFile(mac, ".agents/skills/other/SKILL.md", "other"))
			if mode == "links-on-repo-synced" {
				if err := mac.e.AddEntry(mac.path(repo), AddOptions{}); err != nil {
					t.Fatal(err)
				}
				eventually(t, "u1 has the repo", hasFile(u1, repo+"/tkt-ship/SKILL.md", "mac repo tkt-ship"))
				eventually(t, "u1 reports mac's link blocked by its folder", linkState(u1, ".agents/skills/tkt-work", LinkBlocked))
				eventually(t, "u1 gets the folder link, under its own home", hasLink(u1, ".agents/skills/skills", u1.path(repo)))
			}
			settle(t, u1, u2, mac)

			for _, m := range []*machine{u1, u2} {
				if got := m.read(".agents/skills/tkt-work/SKILL.md"); got != "ubuntu tkt-work" || isSymlink(m.path(".agents/skills/tkt-work")) {
					t.Fatalf("%s's real folder changed: %q", m.name, got)
				}
				if trashed(m, "ubuntu tkt-work") || trashed(m, "other") {
					t.Fatalf("%s trashed a real file", m.name)
				}
				if mode != "links-on-repo-synced" && m.readlink(".agents/skills/skills") != "<no link>" {
					t.Fatalf("%s got the folder link while its target is not synced", m.name)
				}
			}
			if got := mac.read(repo + "/tkt-work/SKILL.md"); got != "mac repo tkt-work" {
				t.Fatalf("written through mac's link: %q", got)
			}
			if got := listDir(t, mac.path(repo+"/tkt-work")); len(got) != 1 {
				t.Fatalf("mac's repo folder changed: %v", got)
			}
			if !isSymlink(mac.path(".agents/skills/tkt-work")) || !isSymlink(mac.path(".agents/skills/skills")) {
				t.Fatal("mac's links were replaced")
			}

			// The newcomer turns tkt-work into a real folder.
			os.Remove(mac.path(".agents/skills/tkt-work"))
			mac.e.Rescan()
			eventually(t, "mac gets the members' tkt-work back", hasFile(mac, ".agents/skills/tkt-work/SKILL.md", "ubuntu tkt-work"))
			settle(t, u1, u2, mac)
			for _, m := range []*machine{u1, u2} {
				if m.read(".agents/skills/tkt-work/SKILL.md") != "ubuntu tkt-work" || trashed(m, "ubuntu tkt-work") {
					t.Fatalf("%s lost tkt-work when mac's link went", m.name)
				}
			}
			if mac.read(repo+"/tkt-work/SKILL.md") != "mac repo tkt-work" {
				t.Fatal("mac's repo copy changed")
			}
			if _, err := os.Lstat(filepath.Join(mac.home, ".agents/skills/tkt-work/SKILL.md")); err != nil {
				t.Fatal(err)
			}
		})
	}
}
