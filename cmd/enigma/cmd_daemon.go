package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/link"
	"github.com/nadavushka-dev/enigma/internal/logfile"
)

// logRotateSize: the daemon log rotates at 10 MB (SPEC §10).
const logRotateSize = 10 << 20

func init() {
	register(&command{name: "daemon", args: "[-v] [--log PATH]", summary: "run the sync daemon in the foreground", run: cmdDaemon})
	register(&command{name: "bridge", summary: "relay an ssh session to the local daemon", hidden: true, run: cmdBridge})
}

func cmdDaemon(c *ctx, args []string) error {
	fs := flags("daemon")
	verbose := fs.Bool("v", false, "")
	logPath := fs.String("log", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{"daemon takes only -v and --log PATH"}
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
	var out io.Writer = c.stderr
	if *logPath != "" {
		w, err := logfile.Open(*logPath, logRotateSize)
		if err != nil {
			return err
		}
		defer w.Close()
		out = w
	}
	log := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: level}))
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
