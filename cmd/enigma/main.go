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
	fmt.Fprintln(w, "\n  enigma <command> -h        help for one command")
	fmt.Fprintln(w, "  enigma -v, --version       print the version")
}

// commandUsage is `enigma <command> -h`: one command, its flags and what it does.
func commandUsage(w io.Writer, c *command) {
	fmt.Fprintf(w, "usage: enigma %s %s\n", c.name, c.args)
	fmt.Fprintf(w, "\n%s\n", c.summary)
	fmt.Fprintln(w, "\nrun `enigma -h` for every command")
}

func printVersion(c *ctx) {
	fmt.Fprintln(c.stdout, "enigma", buildinfo.Version, buildinfo.Platform())
}

func main() {
	os.Exit(run(os.Args[1:], &ctx{stdout: os.Stdout, stderr: os.Stderr, stdin: os.Stdin}))
}

func run(args []string, c *ctx) int {
	if len(args) == 0 && c.stdout == os.Stdout && isTerminal(os.Stdout) && isTerminal(os.Stdin) {
		args = []string{"tui"} // SPEC §12.1: bare enigma in a terminal
	}
	if len(args) == 0 {
		usage(c.stdout)
		return 0
	}
	switch args[0] {
	case "-v", "--version":
		if len(args) != 1 {
			fmt.Fprintln(c.stderr, "enigma: --version takes no arguments")
			return 2
		}
		printVersion(c)
		return 0
	case "-h", "--help":
		usage(c.stdout)
		return 0
	case "help":
		if len(args) > 1 {
			if cmd, ok := commands[args[1]]; ok {
				commandUsage(c.stdout, cmd)
				return 0
			}
		}
		usage(c.stdout)
		return 0
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(c.stderr, "enigma: unknown command %q\n\n", args[0])
		usage(c.stderr)
		return 2
	}
	// `enigma add -h`, `enigma machine add -h`, and even `enigma add --group g -h`
	// show the command's help instead of parsing flags. `--` ends the scan, so
	// a path literally named -h still works: enigma add -- -h.
	for _, a := range args[1:] {
		if a == "--" {
			break
		}
		if a == "-h" || a == "--help" {
			commandUsage(c.stdout, cmd)
			return 0
		}
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
		printVersion(c)
		return nil
	}})
}
