package main

import (
	"fmt"
	"path/filepath"

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
		fmt.Fprintf(c.stdout, "removed %s; the daemon no longer runs in the background\n", service.File(o))
		return nil
	}
	if err := service.Install(o); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "installed %s\nthe daemon now runs in the background and at every login; log: %s\n", service.File(o), o.LogPath)
	return nil
}
