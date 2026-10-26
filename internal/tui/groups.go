package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

// groupsPane is the Groups pane (SPEC §13.2): every group with its
// members and entry count; n new, r rename, x delete, enter members, M
// merge. The cursor browses; `a` makes the group under it the selected
// group (Shared.Target), where the Files pane adds and moves entries.
type groupsPane struct {
	cur    int
	height int
}

func newGroups() *groupsPane { return &groupsPane{} }

func (p *groupsPane) Title() string { return "Groups" }

func (p *groupsPane) Help() []KeyHelp {
	return []KeyHelp{{"a", "add here"}, {"n", "new"}, {"enter", "members"}, {"r", "rename"}, {"x", "delete"}, {"M", "merge"}}
}

func (p *groupsPane) Update(msg tea.Msg, s *Shared) tea.Cmd {
	p.follow(s)
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "a":
		return p.choose(s)
	case "j", "down":
		p.cur = min(p.cur+1, max(0, len(s.Groups)-1))
	case "k", "up":
		p.cur = max(0, p.cur-1)
	case "g":
		p.cur = 0
	case "G":
		p.cur = max(0, len(s.Groups)-1)
	case "n":
		return p.create(s)
	case "r":
		return p.rename(s)
	case "x":
		return p.delete(s)
	case "enter":
		return p.members(s)
	case "M":
		return p.merge(s)
	}
	return nil
}

// follow keeps the cursor inside the list and drops a selection whose
// group vanished, back to main.
func (p *groupsPane) follow(s *Shared) {
	p.cur = max(0, min(p.cur, len(s.Groups)-1))
	if s.Target == "" {
		return
	}
	for _, g := range s.Groups {
		if g.ID == s.Target {
			return
		}
	}
	s.Target = ""
}

// choose makes the group under the cursor the selected group.
func (p *groupsPane) choose(s *Shared) tea.Cmd {
	g, ok := p.current(s)
	if !ok {
		return nil
	}
	s.Target = ""
	if g.ID != proto.MainGroup {
		s.Target = g.ID
	}
	return notice("adding to " + g.Name + ": a in Files adds there, m moves there")
}

func (p *groupsPane) current(s *Shared) (engine.GroupInfo, bool) {
	if p.cur < len(s.Groups) {
		return s.Groups[p.cur], true
	}
	return engine.GroupInfo{}, false
}

// groupByName finds a live group by name; "" is main.
func groupByName(s *Shared, name string) (engine.GroupInfo, error) {
	for _, g := range s.Groups {
		if g.Name == name || (name == "" && g.ID == proto.MainGroup) {
			return g, nil
		}
	}
	return engine.GroupInfo{}, fmt.Errorf("no group named %q", name)
}

func groupNameOf(s *Shared, id string) string {
	for _, g := range s.Groups {
		if g.ID == id {
			return g.Name
		}
	}
	return id
}

// machinePick lists the fleet's members as pick items, checked when in.
func machinePick(s *Shared, in func(id string) bool) ([]PickItem, map[string]string) {
	ids := map[string]string{}
	var items []PickItem
	for _, m := range s.Machines {
		if m.Removed {
			continue
		}
		ids[m.Name] = m.ID
		items = append(items, PickItem{Label: m.Name, Checked: in(m.ID)})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
	return items, ids
}

var refresh tea.Cmd = func() tea.Msg { return RefreshMsg{} }

func notice(text string) tea.Cmd { return func() tea.Msg { return ShowNotice{Text: text} } }

// done refreshes and shows a notice, after an action.
func done(text string) tea.Msg { return tea.BatchMsg{refresh, notice(text)} }

func (p *groupsPane) create(s *Shared) tea.Cmd {
	b := s.Backend
	return func() tea.Msg {
		return OpenPrompt{Title: "New group", Label: "name", Submit: func(name string) tea.Cmd {
			if name == "" {
				return nil
			}
			items, ids := machinePick(s, func(string) bool { return false })
			return func() tea.Msg {
				return OpenPick{Title: "Machines in " + name, Items: items, Submit: func(names []string) tea.Cmd {
					var add []string
					for _, n := range names {
						add = append(add, ids[n])
					}
					return func() tea.Msg {
						if _, err := b.GroupOp(daemon.GroupRequest{Action: "create", Name: name, Add: add}); err != nil {
							return ShowError{Err: err}
						}
						return done("created group " + name)
					}
				}}
			}
		}}
	}
}

func (p *groupsPane) rename(s *Shared) tea.Cmd {
	g, ok := p.current(s)
	if !ok {
		return nil
	}
	if g.ID == proto.MainGroup {
		return showErr(fmt.Errorf("main cannot be renamed"))
	}
	b := s.Backend
	return func() tea.Msg {
		return OpenPrompt{Title: "Rename group " + g.Name, Label: "name", Initial: g.Name, Submit: func(name string) tea.Cmd {
			if name == "" || name == g.Name {
				return nil
			}
			return func() tea.Msg {
				if _, err := b.GroupOp(daemon.GroupRequest{Action: "rename", ID: g.ID, Name: name}); err != nil {
					return ShowError{Err: err}
				}
				return done("renamed group " + g.Name + " to " + name)
			}
		}}
	}
}

// delete removes a group; its entries stop syncing and every machine
// keeps its files, which the confirmation spells out (SPEC §9).
func (p *groupsPane) delete(s *Shared) tea.Cmd {
	g, ok := p.current(s)
	if !ok {
		return nil
	}
	if g.ID == proto.MainGroup {
		return showErr(fmt.Errorf("main cannot be deleted"))
	}
	var paths []string
	for _, e := range s.Status.Entries {
		if e.Group == g.ID {
			slash := ""
			if e.Dir {
				slash = "/"
			}
			paths = append(paths, "~/"+e.Path+slash)
		}
	}
	sort.Strings(paths)
	q := "Delete group " + g.Name + "?"
	if len(paths) > 0 {
		q += fmt.Sprintf(" It has %s on %s: %s. They stop syncing; every machine keeps its files as they are. To keep syncing them everywhere, merge %s into another group instead (M).",
			engine.Plural(len(paths), "synced entry", "synced entries"), strings.Join(g.MemberNames, ", "), strings.Join(paths, ", "), g.Name)
	}
	b := s.Backend
	return func() tea.Msg {
		return OpenConfirm{Question: q, Yes: func() tea.Msg {
			if _, err := b.GroupOp(daemon.GroupRequest{Action: "delete", ID: g.ID}); err != nil {
				return ShowError{Err: err}
			}
			if len(paths) > 0 {
				return done("deleted group " + g.Name + ", " + engine.Plural(len(paths), "entry", "entries") + " no longer synced")
			}
			return done("deleted group " + g.Name)
		}}
	}
}

// members opens the member list; the diff is confirmed, then applied.
func (p *groupsPane) members(s *Shared) tea.Cmd {
	g, ok := p.current(s)
	if !ok {
		return nil
	}
	items, ids := machinePick(s, func(id string) bool { return contains(g.Members, id) })
	b := s.Backend
	return func() tea.Msg {
		return OpenPick{Title: "Machines in " + g.Name, Items: items, Submit: func(names []string) tea.Cmd {
			want := map[string]bool{}
			for _, n := range names {
				want[ids[n]] = true
			}
			var add, remove, plan []string
			for name, id := range ids {
				switch {
				case want[id] && !contains(g.Members, id):
					add = append(add, id)
					plan = append(plan, name+" joins "+g.Name)
				case !want[id] && contains(g.Members, id):
					remove = append(remove, id)
					plan = append(plan, name+" leaves "+g.Name)
				}
			}
			if len(plan) == 0 {
				return nil
			}
			sort.Strings(plan)
			sort.Strings(add)
			sort.Strings(remove)
			return func() tea.Msg {
				return OpenConfirm{Question: strings.Join(plan, ", ") + ". A new member backs up differing files first; a leaving one keeps its files. Apply?",
					Yes: func() tea.Msg {
						if _, err := b.GroupOp(daemon.GroupRequest{Action: "members", ID: g.ID, Add: add, Remove: remove}); err != nil {
							return ShowError{Err: err}
						}
						return done(strings.Join(plan, ", "))
					}}
			}
		}}
	}
}

// merge moves every entry of the group into another, then deletes it
// (SPEC §9), after showing the plan.
func (p *groupsPane) merge(s *Shared) tea.Cmd {
	from, ok := p.current(s)
	if !ok {
		return nil
	}
	if from.ID == proto.MainGroup {
		return showErr(fmt.Errorf("main cannot be merged away"))
	}
	b := s.Backend
	return func() tea.Msg {
		return OpenPrompt{Title: "Merge " + from.Name + " into", Label: "group", Submit: func(name string) tea.Cmd {
			into, err := groupByName(s, name)
			if err != nil {
				return showErr(err)
			}
			if into.ID == from.ID {
				return showErr(fmt.Errorf("a group cannot be merged into itself"))
			}
			machineName := map[string]string{}
			for _, m := range s.Machines {
				machineName[m.ID] = m.Name
			}
			var paths []string
			for _, e := range s.Status.Entries {
				if e.Group == from.ID {
					paths = append(paths, e.Path)
				}
			}
			sort.Strings(paths)
			var gains, losers, loserIDs []string
			for _, id := range into.Members {
				if !contains(from.Members, id) {
					gains = append(gains, machineName[id])
				}
			}
			for _, id := range from.Members {
				if !contains(into.Members, id) {
					losers = append(losers, machineName[id])
					loserIDs = append(loserIDs, id)
				}
			}
			var plan []string
			for _, path := range paths {
				for _, m := range gains {
					plan = append(plan, m+" gains ~/"+path)
				}
			}
			if len(losers) > 0 {
				plan = append(plan, strings.Join(losers, ", ")+" join "+into.Name+" first, keeping their files")
			}
			plan = append(plan, "then "+from.Name+" is deleted")
			home := s.Backend.Home()
			return func() tea.Msg {
				return OpenConfirm{Question: "Merge " + from.Name + " into " + into.Name + ": " + strings.Join(plan, "; ") + ". Go ahead?",
					Yes: func() tea.Msg {
						if len(loserIDs) > 0 {
							if _, err := b.GroupOp(daemon.GroupRequest{Action: "members", ID: into.ID, Add: loserIDs}); err != nil {
								return ShowError{Err: err}
							}
						}
						for _, path := range paths {
							if err := b.MoveEntry(filepath.Join(home, filepath.FromSlash(path)), into.ID); err != nil {
								return ShowError{Err: err}
							}
						}
						if _, err := b.GroupOp(daemon.GroupRequest{Action: "delete", ID: from.ID}); err != nil {
							return ShowError{Err: err}
						}
						return done("merged " + from.Name + " into " + into.Name)
					}}
			}
		}}
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func (p *groupsPane) View(s *Shared, width, height int, focused bool) string {
	p.height = height
	if height <= 0 {
		return ""
	}
	nameW := 4
	for _, g := range s.Groups {
		nameW = max(nameW, len(g.Name))
	}
	lines := make([]string, 0, height)
	for i := 0; i < height; i++ {
		if i >= len(s.Groups) {
			lines = append(lines, fit("", width))
			continue
		}
		g := s.Groups[i]
		members := strings.Join(g.MemberNames, ", ")
		if members == "" {
			members = "(no machines)"
		}
		count := fmt.Sprintf("%d", g.Entries)
		mark := " "
		if g.ID == targetID(s) {
			mark = "▸" // the selected group: adds and moves go here
		}
		left := fmt.Sprintf("%s %-*s  %s", mark, nameW, g.Name, members)
		if focused && i == p.cur {
			lines = append(lines, StyleSelected.Render(fit(padBetween(left, count, width), width)))
		} else {
			lines = append(lines, fit(padBetween(left, StyleDim.Render(count), width), width))
		}
	}
	return strings.Join(lines, "\n")
}
