package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nadavushka-dev/enigma/internal/config"
	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/group"
)

func init() {
	register(&command{name: "machine", args: "add <ssh-alias> [--name NAME] | list [--json] | remove <name> [--uninstall] | rename <name> <new> | alias <name> [<ssh-alias>]",
		summary: "manage the machines in the group", run: cmdMachine})
}

func cmdMachine(c *ctx, args []string) error {
	if len(args) == 0 {
		return usageError{"machine needs add, list, remove, rename or alias"}
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "add":
		return machineAdd(c, args)
	case "list":
		return machineList(c, args)
	case "remove":
		return machineRemove(c, args)
	case "rename":
		return machineRename(c, args)
	case "alias":
		return machineAlias(c, args)
	}
	return usageError{"unknown machine command " + sub}
}

func machineAdd(c *ctx, args []string) error {
	fs := flags("machine add")
	name := fs.String("name", "", "")
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") { // alias first, flags after
		args = append(args[1:], args[0])
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return usageError{"machine add needs exactly one ssh alias"}
	}
	cl, err := client()
	if err != nil {
		return err
	}
	p, _ := paths()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return group.Add(ctx, cl, p, fs.Arg(0), *name, c.stdout)
}

func machineList(c *ctx, args []string) error {
	fs := flags("machine list")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{"machine list takes only --json"}
	}
	cl, err := client()
	if err != nil {
		return err
	}
	ms, err := group.Members(cl)
	if err != nil {
		return err
	}
	st, err := cl.Status()
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(ms)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLINK\tREACHED AT")
	for _, r := range ms {
		if r.ID == st.MachineID {
			fmt.Fprintf(tw, "%s (this machine)\t-\t%s\n", r.Name, r.Target)
			continue
		}
		state, at := "not linked", r.Target
		if pr, ok := group.PeerByName(st.Peers, r.Name); ok {
			if pr.Connected {
				state = "connected"
			}
			if pr.Target != "" {
				at = pr.Target
			}
		}
		if at == "" {
			at = "(it dials this machine)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Name, state, at)
	}
	return tw.Flush()
}

func machineRemove(c *ctx, args []string) error {
	fs := flags("machine remove")
	uninstall := fs.Bool("uninstall", false, "")
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(args[1:], args[0])
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return usageError{"machine remove needs exactly one machine name"}
	}
	cl, err := client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return group.Remove(ctx, cl, fs.Arg(0), *uninstall, c.stdout)
}

func machineRename(c *ctx, args []string) error {
	if len(args) != 2 {
		return usageError{"machine rename needs the current and the new name"}
	}
	cl, err := client()
	if err != nil {
		return err
	}
	ms, err := group.Members(cl)
	if err != nil {
		return err
	}
	r, ok := group.ByName(ms, args[0])
	if !ok {
		return fmt.Errorf("no machine named %q in the group", args[0])
	}
	if _, taken := group.ByName(ms, args[1]); taken {
		return fmt.Errorf("a machine named %q is already in the group", args[1])
	}
	r.Name = args[1]
	if err := cl.SetMachine(r); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "renamed %s to %s on every machine\n", args[0], args[1])
	return nil
}

func machineAlias(c *ctx, args []string) error {
	if len(args) != 1 && len(args) != 2 {
		return usageError{"machine alias needs a machine name and, to set it, an ssh alias"}
	}
	p, err := paths()
	if err != nil {
		return err
	}
	alias := ""
	if len(args) == 2 {
		alias = args[1]
	}
	if err := config.SetAlias(p, args[0], alias); err != nil {
		return err
	}
	daemon.NewClient(p).Reload() // fine if it is not running
	if alias == "" {
		fmt.Fprintf(c.stdout, "this machine reaches %s the usual way again\n", args[0])
	} else {
		fmt.Fprintf(c.stdout, "this machine now reaches %s with `ssh %s`\n", args[0], alias)
	}
	return nil
}
