// Command enigma keeps chosen files and folders identical across your
// machines, in real time, over ssh. See SPEC.md.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/nadavushka-dev/enigma/internal/buildinfo"
)

// command is one subcommand; each registers itself from its own file.
type command struct {
	name    string
	args    string // usage after the name
	summary string
	hidden  bool // internal, not listed in help
	run     func(c *ctx, args []string) error
}

var commands = map[string]*command{}

func register(c *command) { commands[c.name] = c }

// ctx carries the process's streams, so commands are easy to test.
type ctx struct {
	stdout, stderr io.Writer
	stdin          io.Reader
}

// usageError makes main exit with status 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usage(w io.Writer) {
	fmt.Fprintln(w, "enigma — keep chosen files and folders identical across your machines, over ssh")
	fmt.Fprintln(w, "\nUsage:")
	names := make([]string, 0, len(commands))
	for n, c := range commands {
		if !c.hidden {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		c := commands[n]
		fmt.Fprintf(w, "  enigma %-34s %s\n", strings.TrimSpace(n+" "+c.args), c.summary)
	}
}

func main() {
	os.Exit(run(os.Args[1:], &ctx{stdout: os.Stdout, stderr: os.Stderr, stdin: os.Stdin}))
}

func run(args []string, c *ctx) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(c.stdout)
		return 0
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(c.stderr, "enigma: unknown command %q\n\n", args[0])
		usage(c.stderr)
		return 2
	}
	if err := cmd.run(c, args[1:]); err != nil {
		fmt.Fprintln(c.stderr, "enigma:", err)
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(c.stderr, "usage: enigma %s %s\n", cmd.name, cmd.args)
			return 2
		}
		return 1
	}
	return 0
}

func init() {
	register(&command{name: "version", summary: "print the version", run: func(c *ctx, _ []string) error {
		fmt.Fprintln(c.stdout, "enigma", buildinfo.Version, buildinfo.Platform())
		return nil
	}})
}
