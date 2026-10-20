package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nadavushka-dev/enigma/internal/buildinfo"
	"github.com/nadavushka-dev/enigma/internal/config"
	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/link"
	"github.com/nadavushka-dev/enigma/internal/proto"
	"github.com/nadavushka-dev/enigma/internal/release"
	"github.com/nadavushka-dev/enigma/internal/sshconfig"
)

// remoteBin is where machine add installs enigma (SPEC §11 step 3).
const remoteBin = "~/.local/bin/enigma"

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

// members are the machine records not removed, and this machine's identity.
func members(cl *daemon.Client) ([]proto.MachineRecord, error) {
	all, err := cl.Machines()
	if err != nil {
		return nil, err
	}
	var out []proto.MachineRecord
	for _, r := range all {
		if !r.Removed {
			out = append(out, r)
		}
	}
	return out, nil
}

func byName(rs []proto.MachineRecord, name string) (proto.MachineRecord, bool) {
	for _, r := range rs {
		if r.Name == name {
			return r, true
		}
	}
	return proto.MachineRecord{}, false
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
	alias := fs.Arg(0)
	if *name == "" {
		*name = alias
	}
	cl, err := client()
	if err != nil {
		return err
	}
	p, _ := paths()
	id, err := config.LoadIdentity(p)
	if err != nil {
		return err
	}
	ms, err := members(cl)
	if err != nil {
		return err
	}
	for _, r := range ms {
		if r.Name == *name {
			return fmt.Errorf("a machine named %q is already in the group (use --name)", *name)
		}
		if r.Alias == alias {
			return fmt.Errorf("%s is already in the group, as %s", alias, r.Name)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	step := func(n int, what, cmd string) {
		fmt.Fprintf(c.stdout, "%d. %s\n   $ %s\n", n, what, cmd)
	}

	step(1, "detect the platform", "ssh "+alias+" uname -sm")
	out, err := link.Shell(ctx, alias, "uname -sm", nil)
	if err != nil {
		return err
	}
	platform := buildinfo.PlatformFromUname(string(out))
	if platform == "" {
		return fmt.Errorf("%s runs %q: enigma supports macOS and Linux on amd64 and arm64", alias, strings.TrimSpace(string(out)))
	}
	fmt.Fprintf(c.stdout, "   %s\n", platform)

	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	if err != nil {
		return err
	}
	binary, src, err := release.BinaryFor(ctx, release.Default(), p.Data, self, buildinfo.Version, buildinfo.Platform(), platform)
	if err != nil {
		return err
	}
	step(2, "copy enigma "+buildinfo.Version+" ("+src+")", "ssh "+alias+" 'mkdir -p ~/.local/bin && cat > ~/.local/bin/.enigma-new && mv -f ~/.local/bin/.enigma-new "+remoteBin+"'")
	install := `mkdir -p ~/.local/bin && t=~/.local/bin/.enigma-new.$$ && cat > "$t" && chmod 755 "$t" && mv -f "$t" ` + remoteBin + ` && ` + remoteBin + ` version`
	out, err = link.Shell(ctx, alias, install, bytes.NewReader(binary))
	if err != nil {
		return err
	}
	if f := strings.Fields(string(out)); len(f) < 2 || f[1] != buildinfo.Version {
		return fmt.Errorf("the installed binary on %s says %q, not %s", alias, strings.TrimSpace(string(out)), buildinfo.Version)
	}

	step(3, "record how to reach it", "ssh -G "+alias)
	target, err := link.Resolve(ctx, alias)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "   %s\n", target)
	rec := proto.MachineRecord{ID: config.NewID(), Name: *name, Alias: alias, Target: target, Bin: remoteBin}

	step(4, "join the group (the group is sent on stdin)", "ssh "+alias+" "+remoteBin+" join")
	req, _ := json.Marshal(joinRequest{Identity: config.Identity{MachineID: rec.ID, GroupID: id.GroupID, Name: *name}, Members: append(ms, rec)})
	if _, err := link.Shell(ctx, alias, remoteBin+" join", bytes.NewReader(req)); err != nil {
		return err
	}

	step(5, "record "+*name+" in the group; every machine links to it", "(this machine's daemon)")
	if err := cl.SetMachine(rec); err != nil {
		return err
	}

	step(6, "start its daemon", "ssh "+alias+" "+remoteBin+" service install")
	out, err = link.Shell(ctx, alias, remoteBin+" service install", nil)
	if err != nil {
		return err
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fmt.Fprintf(c.stdout, "   %s\n", l)
	}

	step(7, "check its links", "ssh "+alias+" "+remoteBin+" status --json")
	peers := remoteLinks(ctx, alias, id.Name)
	missing := 0
	for _, pr := range peers {
		switch {
		case pr.Connected:
			fmt.Fprintf(c.stdout, "   connected to %s\n", pr.Name)
		case pr.Target == "":
			fmt.Fprintf(c.stdout, "   %s: not linked yet (it has no address for %s; %s dials it)\n", pr.Name, pr.Name, pr.Name)
		default:
			missing++
			fmt.Fprintf(c.stdout, "   warning: not connected to %s: %s\n", pr.Name, pr.LastError)
		}
	}
	if missing > 0 {
		fmt.Fprintln(c.stdout, "   changes still reach it through the machines it is linked to")
	}
	fmt.Fprintf(c.stdout, "done: %s is in the group\n", *name)
	return nil
}

// remoteLinks waits until the new machine is linked to this one (a few
// seconds more for the others) and returns its view of its peers.
func remoteLinks(ctx context.Context, alias, self string) []daemon.PeerReport {
	var st daemon.StatusReport
	read := func() bool {
		out, err := link.Shell(ctx, alias, remoteBin+" status --json", nil)
		return err == nil && json.Unmarshal(out, &st) == nil
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if read() {
			if pr, ok := peerByName(st.Peers, self); ok && pr.Connected {
				time.Sleep(2 * time.Second) // let the other links come up
				read()
				return st.Peers
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return st.Peers
}

func peerByName(ps []daemon.PeerReport, name string) (daemon.PeerReport, bool) {
	for _, p := range ps {
		if p.Name == name {
			return p, true
		}
	}
	return daemon.PeerReport{}, false
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
	ms, err := members(cl)
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
		if pr, ok := peerByName(st.Peers, r.Name); ok {
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
	name := fs.Arg(0)
	cl, err := client()
	if err != nil {
		return err
	}
	ms, err := members(cl)
	if err != nil {
		return err
	}
	r, ok := byName(ms, name)
	if !ok {
		return fmt.Errorf("no machine named %q in the group (see `enigma machine list`)", name)
	}
	st, err := cl.Status()
	if err != nil {
		return err
	}
	if r.ID == st.MachineID {
		return fmt.Errorf("%s is this machine: remove it from another member", name)
	}
	r.Removed = true
	if err := cl.SetMachine(r); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "removed %s from the group; no machine links to it anymore\n", name)

	leave := r.Bin + " leave"
	if r.Bin == "" {
		leave = "enigma leave"
	}
	if *uninstall {
		leave += " --uninstall"
	}
	targets := targetsOf(r)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var lastErr error
	for _, t := range targets {
		fmt.Fprintf(c.stdout, "telling it to leave\n   $ ssh %s %s\n", t, leave)
		out, err := link.Shell(ctx, t, leave, nil)
		if err == nil {
			fmt.Fprintf(c.stdout, "   %s: %s\n", name, strings.TrimSpace(string(out)))
			return nil
		}
		lastErr = err
	}
	if lastErr != nil {
		fmt.Fprintf(c.stdout, "warning: could not reach %s (%v); it keeps running there until you run `enigma leave` on it\n", name, lastErr)
	} else {
		fmt.Fprintf(c.stdout, "warning: no address for %s; run `enigma leave` on it\n", name)
	}
	return nil
}

func machineRename(c *ctx, args []string) error {
	if len(args) != 2 {
		return usageError{"machine rename needs the current and the new name"}
	}
	cl, err := client()
	if err != nil {
		return err
	}
	ms, err := members(cl)
	if err != nil {
		return err
	}
	r, ok := byName(ms, args[0])
	if !ok {
		return fmt.Errorf("no machine named %q in the group", args[0])
	}
	if _, taken := byName(ms, args[1]); taken {
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

// targetsOf is how this machine reaches r, as its daemon would (SPEC §5).
func targetsOf(r proto.MachineRecord) []string {
	home, _ := os.UserHomeDir()
	overrides := map[string]string{}
	if p, err := paths(); err == nil {
		if l, err := config.LoadLocal(p); err == nil {
			overrides = l.Aliases
		}
	}
	return daemon.Targets(r, overrides, func(a string) bool { return sshconfig.Has(home, a) })
}
