package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nadavushka-dev/enigma/internal/config"
	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/group"
	"github.com/nadavushka-dev/enigma/internal/service"
)

func init() {
	register(&command{name: "init", args: "[--name NAME]", summary: "create a group with this machine as its first member", run: cmdInit})
	register(&command{name: "join", summary: "join a group; the group arrives on stdin (run by machine add)", hidden: true, run: cmdJoin})
	register(&command{name: "leave", args: "[--uninstall]", summary: "stop and forget the group (run by machine remove)", hidden: true, run: cmdLeave})
	register(&command{name: "identity", summary: "print this machine's identity as JSON", hidden: true, run: cmdIdentity})
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
	fmt.Fprintln(c.stdout, "next: `enigma service install` to run the daemon, then `enigma machine add <ssh-alias>` for each other machine")
	return nil
}

func cmdJoin(c *ctx, args []string) error {
	var req group.JoinRequest
	if err := json.NewDecoder(c.stdin).Decode(&req); err != nil {
		return fmt.Errorf("join: reading the group from stdin: %w", err)
	}
	if req.Identity.MachineID == "" || req.Identity.GroupID == "" {
		return errors.New("join: no identity in the request")
	}
	p, err := paths()
	if err != nil {
		return err
	}
	if err := config.Join(p, req.Identity, req.Members); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "joined group %s as %s\n", short(req.Identity.GroupID), req.Identity.Name)
	return nil
}

func cmdLeave(c *ctx, args []string) error {
	fs := flags("leave")
	uninstall := fs.Bool("uninstall", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{"leave takes only --uninstall"}
	}
	p, err := paths()
	if err != nil {
		return err
	}
	if err := stopDaemon(p); err != nil {
		return err
	}
	if *uninstall {
		o, err := service.Defaults(filepath.Join(p.Data, "enigma.log"), filepath.Join(p.Data, "daemon.out"))
		if err != nil {
			return err
		}
		if err := service.Uninstall(o); err != nil {
			return err
		}
	}
	if err := config.Leave(p); err != nil {
		return err
	}
	msg := "left the group; files stay as they are"
	if *uninstall {
		home, _ := os.UserHomeDir()
		installed := filepath.Join(home, ".local", "bin", "enigma")
		if self, err := os.Executable(); err == nil {
			if self, _ = filepath.EvalSymlinks(self); self == installed {
				os.Remove(installed)
			}
		}
		msg += "; enigma is uninstalled"
	}
	fmt.Fprintln(c.stdout, msg)
	return nil
}

// stopDaemon asks a running daemon to stop and waits until it has.
func stopDaemon(p config.Paths) error {
	cl := daemon.NewClient(p)
	if _, err := cl.Status(); err != nil {
		return nil // not running
	}
	if err := cl.Shutdown(); err != nil {
		return err
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if _, err := cl.Status(); err != nil {
			return nil
		}
	}
	return errors.New("the daemon did not stop")
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

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
