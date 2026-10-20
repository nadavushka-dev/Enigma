package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/link"
)

func init() {
	register(&command{name: "daemon", args: "[-v]", summary: "run the sync daemon in the foreground", run: cmdDaemon})
	register(&command{name: "bridge", summary: "relay an ssh session to the local daemon", hidden: true, run: cmdBridge})
}

func cmdDaemon(c *ctx, args []string) error {
	fs := flags("daemon")
	verbose := fs.Bool("v", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{"daemon takes only -v"}
	}
	p, err := paths()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(c.stderr, &slog.HandlerOptions{Level: level}))
	sigctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return daemon.Run(sigctx, daemon.Options{Paths: p, Home: home, Log: log})
}

func cmdBridge(c *ctx, args []string) error {
	p, err := paths()
	if err != nil {
		return err
	}
	return link.Bridge(c.stdin, c.stdout, p.PeerSocket())
}
