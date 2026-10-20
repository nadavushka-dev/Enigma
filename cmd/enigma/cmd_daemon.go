package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
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
	var logw *logfile.Writer
	if *logPath != "" {
		if logw, err = logfile.Open(*logPath, logRotateSize); err != nil {
			return err
		}
		out = logw
	}
	log := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: level}))
	// Resolved now: after an upgrade renames over it, Linux reports the old,
	// deleted inode as this executable.
	bin, err := os.Executable()
	if err == nil {
		bin, err = filepath.EvalSymlinks(bin)
	}
	if err != nil {
		return err
	}
	sigctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err = daemon.Run(sigctx, daemon.Options{Paths: p, Home: home, Log: log, Bin: bin})
	stop()
	if logw != nil {
		logw.Close()
	}
	if errors.Is(err, daemon.ErrRestart) {
		// Upgraded in place (SPEC §11): become the new binary, same
		// arguments, same process (launchd and systemd keep tracking it).
		return syscall.Exec(bin, append([]string{bin}, os.Args[1:]...), os.Environ())
	}
	return err
}

func cmdBridge(c *ctx, args []string) error {
	p, err := paths()
	if err != nil {
		return err
	}
	return link.Bridge(c.stdin, c.stdout, p.PeerSocket())
}
