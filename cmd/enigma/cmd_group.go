package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nadavushka-dev/enigma/internal/config"
	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/link"
)

func init() {
	register(&command{name: "init", args: "[--name NAME]", summary: "create a group with this machine as its first member", run: cmdInit})
	register(&command{name: "join", args: "[--name NAME] [--remote-cmd CMD] <ssh-alias>", summary: "join the group of a machine you can ssh to", run: cmdJoin})
	register(&command{name: "peers", args: "[remove <name>]", summary: "list the machines this one dials, or stop dialing one", run: cmdPeers})
	register(&command{name: "identity", summary: "print this machine's identity as JSON (used by join)", hidden: true, run: cmdIdentity})
}

func paths() (config.Paths, error) { return config.Default() }

func defaultName() string {
	h, err := os.Hostname()
	if err != nil {
		return "machine"
	}
	return strings.SplitN(h, ".", 2)[0]
}

func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func cmdInit(c *ctx, args []string) error {
	fs := flags("init")
	name := fs.String("name", defaultName(), "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{"init takes only --name"}
	}
	p, err := paths()
	if err != nil {
		return err
	}
	id, err := config.Init(p, *name)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "created group %s with this machine (%s) as its first member\n", short(id.GroupID), id.Name)
	fmt.Fprintln(c.stdout, "next: start the daemon (`enigma daemon`), then run `enigma join <this-machine>` on your other machines")
	return nil
}

func cmdJoin(c *ctx, args []string) error {
	fs := flags("join")
	name := fs.String("name", defaultName(), "")
	remoteCmd := fs.String("remote-cmd", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return usageError{"join needs exactly one ssh alias"}
	}
	target := fs.Arg(0)
	p, err := paths()
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := link.Run(cctx, target, *remoteCmd, "identity")
	if err != nil {
		return fmt.Errorf("%w\nhint: enigma must be installed on %s and on the PATH of non-interactive ssh sessions (or pass --remote-cmd /path/to/enigma)", err, target)
	}
	var remote config.Identity
	if err := json.Unmarshal(out, &remote); err != nil || remote.GroupID == "" {
		return fmt.Errorf("%s is not in a group yet: run `enigma init` there first", target)
	}
	id, err := config.Join(p, remote.GroupID, *name)
	if err != nil {
		return err
	}
	if err := config.AddPeer(p, config.Peer{Name: remote.Name, Target: target, RemoteCmd: *remoteCmd}); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "joined group %s as %s; will link to %s (%s)\n", short(id.GroupID), id.Name, remote.Name, target)
	if err := daemon.NewClient(p).Reload(); err != nil {
		fmt.Fprintln(c.stdout, "next: start the daemon (`enigma daemon`)")
	}
	return nil
}

func cmdIdentity(c *ctx, args []string) error {
	p, err := paths()
	if err != nil {
		return err
	}
	id, err := config.LoadIdentity(p)
	if err != nil {
		return err
	}
	return json.NewEncoder(c.stdout).Encode(id)
}

func cmdPeers(c *ctx, args []string) error {
	p, err := paths()
	if err != nil {
		return err
	}
	if len(args) == 2 && args[0] == "remove" {
		if err := config.RemovePeer(p, args[1]); err != nil {
			return err
		}
		daemon.NewClient(p).Reload()
		fmt.Fprintf(c.stdout, "no longer dialing %s\n", args[1])
		return nil
	}
	if len(args) != 0 {
		return usageError{"unknown peers arguments"}
	}
	peers, err := config.LoadPeers(p)
	if err != nil {
		return err
	}
	if len(peers) == 0 {
		fmt.Fprintln(c.stdout, "this machine dials no peers (machines that dial it still link)")
		return nil
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSSH TARGET\tREMOTE COMMAND")
	for _, pr := range peers {
		rc := pr.RemoteCmd
		if rc == "" {
			rc = "enigma"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", pr.Name, pr.Target, rc)
	}
	return tw.Flush()
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
