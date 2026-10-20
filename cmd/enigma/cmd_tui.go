package main

import (
	"errors"
	"os"

	"github.com/nadavushka-dev/enigma/internal/tui"
)

func init() {
	register(&command{name: "tui", summary: "the terminal UI (also what bare `enigma` opens in a terminal)", run: cmdTUI})
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func cmdTUI(c *ctx, args []string) error {
	if len(args) != 0 {
		return usageError{"tui takes no arguments"}
	}
	if !isTerminal(os.Stdout) || !isTerminal(os.Stdin) {
		return errors.New("the TUI needs a terminal; use the other commands in scripts (see `enigma help`)")
	}
	p, err := paths()
	if err != nil {
		return err
	}
	b, err := tui.NewLive(p)
	if err != nil {
		return err
	}
	return tui.Run(b)
}
