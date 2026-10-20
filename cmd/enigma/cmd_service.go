package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/service"
)

func init() {
	register(&command{name: "service", args: "install|uninstall", summary: "run the daemon in the background at login (launchd / systemd)", run: cmdService})
}

func cmdService(c *ctx, args []string) error {
	if len(args) != 1 || (args[0] != "install" && args[0] != "uninstall") {
		return usageError{"service needs install or uninstall"}
	}
	p, err := paths()
	if err != nil {
		return err
	}
	o, err := service.Defaults(filepath.Join(p.Data, "enigma.log"), filepath.Join(p.Data, "daemon.out"))
	if err != nil {
		return err
	}
	if args[0] == "uninstall" {
		if err := service.Uninstall(o); err != nil {
			return err
		}
		if err := stopDaemon(p); err != nil { // a background daemon has no service manager to stop it
			return err
		}
		fmt.Fprintln(c.stdout, "the daemon is stopped and no longer starts by itself")
		return nil
	}
	res, err := service.Install(o)
	if err != nil {
		return err
	}
	// Wait for it: a daemon that cannot start (no identity, ...) should
	// fail here, not silently.
	cl := daemon.NewClient(p)
	up := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if _, err := cl.Status(); err == nil {
			up = true
			break
		}
	}
	if !up {
		return errors.New("the daemon did not start; see " + o.LogPath)
	}
	switch res.Mode {
	case "background":
		fmt.Fprintf(c.stdout, "the daemon runs in the background; log: %s\n", o.LogPath)
	default:
		fmt.Fprintf(c.stdout, "installed %s (%s)\nthe daemon now runs in the background and at every login; log: %s\n", service.File(o), res.Mode, o.LogPath)
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(c.stdout, "warning:", w)
	}
	return nil
}
