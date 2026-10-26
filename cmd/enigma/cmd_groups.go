package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/fleet"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

func init() {
	register(&command{name: "group", args: "list [--json] | create <name> [machine]... | delete <name> | rename <name> <new> | add <name> <machine>... | remove <name> <machine>... | merge <from> <into> [--yes] [--add-members]",
		summary: "groups: which machines sync which entries (SPEC §9)", run: cmdGroup})
	register(&command{name: "entry", args: "move <path> <group>", summary: "move a synced file or folder to another group", run: cmdEntry})
}

func cmdGroup(c *ctx, args []string) error {
	if len(args) == 0 {
		return usageError{"group needs list, create, delete, rename, add, remove or merge"}
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "list":
		return groupList(c, args)
	case "create":
		return groupCreate(c, args)
	case "delete":
		return groupDelete(c, args)
	case "rename":
		return groupRename(c, args)
	case "add", "remove":
		return groupMembers(c, sub, args)
	case "merge":
		return groupMerge(c, args)
	}
	return usageError{"unknown group command " + sub}
}

// groupNamed finds a live group by name.
func groupNamed(cl *daemon.Client, name string) (engine.GroupInfo, error) {
	gs, err := cl.Groups()
	if err != nil {
		return engine.GroupInfo{}, err
	}
	g, ok := fleet.GroupByName(gs, name)
	if !ok {
		return engine.GroupInfo{}, fmt.Errorf("no group named %q (enigma group list shows them)", name)
	}
	return g, nil
}

// machineIDs turns machine names into IDs.
func machineIDs(cl *daemon.Client, names []string) ([]string, error) {
	ms, err := fleet.Members(cl)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, n := range names {
		r, ok := fleet.ByName(ms, n)
		if !ok {
			return nil, fmt.Errorf("no machine named %q in the fleet", n)
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
}

func groupList(c *ctx, args []string) error {
	fs := flags("group list")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{"group list takes only --json"}
	}
	cl, err := client()
	if err != nil {
		return err
	}
	gs, err := cl.Groups()
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(gs)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "GROUP\tMACHINES\tENTRIES")
	for _, g := range gs {
		members := strings.Join(g.MemberNames, ", ")
		if members == "" {
			members = "(none)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\n", g.Name, members, g.Entries)
	}
	return tw.Flush()
}

func groupCreate(c *ctx, args []string) error {
	if len(args) == 0 {
		return usageError{"group create needs a name, then the machines in it"}
	}
	cl, err := client()
	if err != nil {
		return err
	}
	ids, err := machineIDs(cl, args[1:])
	if err != nil {
		return err
	}
	if _, err := cl.Group(daemon.GroupRequest{Action: "create", Name: args[0], Add: ids}); err != nil {
		return err
	}
	with := "no machines yet"
	if len(args) > 1 {
		with = strings.Join(args[1:], ", ")
	}
	fmt.Fprintf(c.stdout, "created group %s with %s\n", args[0], with)
	return nil
}

func groupDelete(c *ctx, args []string) error {
	if len(args) != 1 {
		return usageError{"group delete needs exactly one group name"}
	}
	cl, err := client()
	if err != nil {
		return err
	}
	if args[0] == "main" {
		return fmt.Errorf("main cannot be deleted")
	}
	g, err := groupNamed(cl, args[0])
	if err != nil {
		return err
	}
	if _, err := cl.Group(daemon.GroupRequest{Action: "delete", ID: g.ID}); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "deleted group %s on every machine\n", g.Name)
	return nil
}

func groupRename(c *ctx, args []string) error {
	if len(args) != 2 {
		return usageError{"group rename needs the current and the new name"}
	}
	cl, err := client()
	if err != nil {
		return err
	}
	g, err := groupNamed(cl, args[0])
	if err != nil {
		return err
	}
	if _, err := cl.Group(daemon.GroupRequest{Action: "rename", ID: g.ID, Name: args[1]}); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "renamed group %s to %s on every machine\n", args[0], args[1])
	return nil
}

func groupMembers(c *ctx, verb string, args []string) error {
	if len(args) < 2 {
		return usageError{"group " + verb + " needs a group name, then machine names"}
	}
	cl, err := client()
	if err != nil {
		return err
	}
	g, err := groupNamed(cl, args[0])
	if err != nil {
		return err
	}
	ids, err := machineIDs(cl, args[1:])
	if err != nil {
		return err
	}
	req := daemon.GroupRequest{Action: "members", ID: g.ID}
	if verb == "add" {
		req.Add = ids
	} else {
		req.Remove = ids
	}
	if _, err := cl.Group(req); err != nil {
		return err
	}
	if verb == "add" {
		fmt.Fprintf(c.stdout, "%s now in group %s: its entries sync there (a differing local copy is backed up first)\n", strings.Join(args[1:], ", "), g.Name)
	} else {
		fmt.Fprintf(c.stdout, "%s out of group %s: its entries stop syncing there, files stay\n", strings.Join(args[1:], ", "), g.Name)
	}
	return nil
}

// groupMerge moves every entry of one group into another, then deletes
// the first (SPEC §9). It prints the plan and asks, unless --yes.
func groupMerge(c *ctx, args []string) error {
	fs := flags("group merge")
	yes := fs.Bool("yes", false, "")
	addMembers := fs.Bool("add-members", false, "")
	var pos []string
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		pos, args = append(pos, args[0]), args[1:]
	}
	if err := fs.Parse(args); err != nil || len(pos)+fs.NArg() != 2 {
		return usageError{"group merge needs the group to merge and the group to merge it into"}
	}
	pos = append(pos, fs.Args()...)
	if pos[0] == pos[1] {
		return fmt.Errorf("a group cannot be merged into itself")
	}
	if pos[0] == "main" {
		return fmt.Errorf("main cannot be merged away")
	}
	cl, err := client()
	if err != nil {
		return err
	}
	from, err := groupNamed(cl, pos[0])
	if err != nil {
		return err
	}
	into, err := groupNamed(cl, pos[1])
	if err != nil {
		return err
	}
	entries, err := cl.Entries()
	if err != nil {
		return err
	}
	var paths []string
	for _, e := range entries {
		if e.Group == from.ID {
			paths = append(paths, e.Path)
		}
	}
	sort.Strings(paths)
	name := map[string]string{}
	ms, _ := fleet.Members(cl)
	for _, m := range ms {
		name[m.ID] = m.Name
	}
	has := func(g engine.GroupInfo, id string) bool { return g.GroupRecord.Has(id) || contains(g.Members, id) }
	var gains, losers []string
	for _, id := range into.Members {
		if !has(from, id) {
			gains = append(gains, name[id])
		}
	}
	for _, id := range from.Members {
		if !has(into, id) {
			losers = append(losers, name[id])
		}
	}
	home := func(p string) string { return "~/" + p }
	fmt.Fprintf(c.stdout, "merge %s into %s: %d entries move, then %s is deleted\n", from.Name, into.Name, len(paths), from.Name)
	for _, p := range paths {
		for _, m := range gains {
			fmt.Fprintf(c.stdout, "  %s gains %s\n", m, home(p))
		}
		for _, m := range losers {
			if *addMembers {
				fmt.Fprintf(c.stdout, "  %s keeps %s (%s joins %s)\n", m, home(p), m, into.Name)
			} else {
				fmt.Fprintf(c.stdout, "  %s would lose %s\n", m, home(p))
			}
		}
	}
	if len(losers) > 0 && !*addMembers {
		return fmt.Errorf("%s would stop syncing; pass --add-members to add %s to %s first", strings.Join(losers, ", "), strings.Join(losers, ", "), into.Name)
	}
	if !*yes {
		if !isTerminal(os.Stdin) {
			return fmt.Errorf("pass --yes to merge without a terminal")
		}
		fmt.Fprint(c.stdout, "merge? [y/N] ")
		line, _ := bufio.NewReader(c.stdin).ReadString('\n')
		if l := strings.ToLower(strings.TrimSpace(line)); l != "y" && l != "yes" {
			fmt.Fprintln(c.stdout, "nothing changed")
			return nil
		}
	}
	if len(losers) > 0 {
		ids, err := machineIDs(cl, losers)
		if err != nil {
			return err
		}
		if _, err := cl.Group(daemon.GroupRequest{Action: "members", ID: into.ID, Add: ids}); err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "%s joins %s\n", strings.Join(losers, ", "), into.Name)
	}
	home0, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	for _, p := range paths {
		if err := cl.MoveEntry(home0+"/"+p, into.ID); err != nil {
			return err
		}
	}
	if _, err := cl.Group(daemon.GroupRequest{Action: "delete", ID: from.ID}); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "merged %s into %s\n", from.Name, into.Name)
	return nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func cmdEntry(c *ctx, args []string) error {
	if len(args) != 3 || args[0] != "move" {
		return usageError{"entry move needs a path and a group"}
	}
	abs, err := absPath(args[1])
	if err != nil {
		return err
	}
	cl, err := client()
	if err != nil {
		return err
	}
	id := proto.MainGroup
	if args[2] != "main" {
		g, err := groupNamed(cl, args[2])
		if err != nil {
			return err
		}
		id = g.ID
	}
	if err := cl.MoveEntry(abs, id); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "moved %s to %s\n", tilde(abs), args[2])
	return nil
}
