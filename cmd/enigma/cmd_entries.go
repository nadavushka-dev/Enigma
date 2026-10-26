package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nadavushka-dev/enigma/internal/config"
	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/fuzzy"
	"github.com/nadavushka-dev/enigma/internal/problems"
)

func init() {
	register(&command{name: "add", args: "[--ignore GLOB]... [path]", summary: "sync a file or folder on every machine (no path: pick with fzf)", run: cmdAdd})
	register(&command{name: "remove", args: "<path>", summary: "stop syncing a file or folder (files stay everywhere)", run: cmdRemove})
	register(&command{name: "list", summary: "list synced files and folders", run: cmdList})
	register(&command{name: "status", args: "[--json]", summary: "machines, links, synced entries and conflicts", run: cmdStatus})
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// absPath resolves ~ and relative paths the way a shell user expects.
func absPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	return filepath.Abs(p)
}

func client() (*daemon.Client, error) {
	p, err := paths()
	if err != nil {
		return nil, err
	}
	if _, err := config.LoadIdentity(p); err != nil {
		return nil, err
	}
	return daemon.NewClient(p), nil
}

// adder is what `add` needs from the daemon; tests fake it.
type adder interface {
	Add(abs string, ignore []string, group string) error
}

// The seams of `add` with no path (SPEC §12.4), replaced in tests.
var (
	entryAdder       = func() (adder, error) { return client() }
	pickerIsTerminal = func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) }
	haveFzf          = func() bool { _, err := exec.LookPath("fzf"); return err == nil }
	runFzf           = fzfMulti
)

var errNoPicker = errors.New("give a path, or install fzf to pick one")

func cmdAdd(c *ctx, args []string) error {
	fs := flags("add")
	var ignore multi
	fs.Var(&ignore, "ignore", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 1 {
		return usageError{"add takes one path, or none to pick with fzf"}
	}
	var picks []string
	if fs.NArg() == 1 {
		picks = []string{fs.Arg(0)}
	} else {
		var err error
		if picks, err = pickWithFzf(c); err != nil || len(picks) == 0 {
			return err
		}
	}
	cl, err := entryAdder()
	if err != nil {
		return err
	}
	for _, p := range picks {
		abs, err := absPath(p)
		if err != nil {
			return err
		}
		if err := cl.Add(abs, ignore, ""); err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "syncing %s on every machine\n", tilde(abs)+trailingSlash(p))
	}
	return nil
}

// pickWithFzf pipes the home index into `fzf --multi` and returns the
// picks as paths under ~ (nil when nothing was picked).
func pickWithFzf(c *ctx) ([]string, error) {
	if !pickerIsTerminal() || !haveFzf() {
		return nil, errNoPicker
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	p, err := paths()
	if err != nil {
		return nil, err
	}
	ix := fuzzy.Build(home, fuzzy.Options{Skip: []string{p.Data, p.Config}})
	ents, pr := ix.Wait()
	if pr.Truncated {
		fmt.Fprintf(c.stderr, "enigma: showing the first %d paths under ~\n", pr.Count)
	}
	lines, err := runFzf(fuzzy.Paths(ents))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, filepath.Join(home, filepath.FromSlash(l))+trailingSlash(l))
		}
	}
	return out, nil
}

func trailingSlash(s string) string {
	if strings.HasSuffix(s, "/") {
		return "/"
	}
	return ""
}

// fzfMulti runs fzf over lines and returns the chosen ones; a cancelled
// pick (exit 130) or no match (exit 1) is simply nothing.
func fzfMulti(lines []string) ([]string, error) {
	cmd := exec.Command("fzf", "--multi", "--prompt", "add> ")
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	cmd.Stderr = os.Stderr
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && (ee.ExitCode() == 1 || ee.ExitCode() == 130) {
			return nil, nil
		}
		return nil, fmt.Errorf("fzf: %w", err)
	}
	return strings.Split(strings.TrimRight(out.String(), "\n"), "\n"), nil
}

func cmdRemove(c *ctx, args []string) error {
	if len(args) != 1 {
		return usageError{"remove needs exactly one path"}
	}
	abs, err := absPath(args[0])
	if err != nil {
		return err
	}
	cl, err := client()
	if err != nil {
		return err
	}
	if err := cl.Remove(abs); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "stopped syncing %s (files stay on every machine)\n", tilde(abs))
	return nil
}

func cmdList(c *ctx, args []string) error {
	cl, err := client()
	if err != nil {
		return err
	}
	st, err := cl.Status()
	if err != nil {
		return err
	}
	if len(st.Entries) == 0 {
		fmt.Fprintln(c.stdout, "nothing is synced yet; add something with `enigma add <path>`")
		return nil
	}
	for _, e := range st.Entries {
		fmt.Fprintln(c.stdout, entryLabel(e.Path, e.Dir))
	}
	return nil
}

func cmdStatus(c *ctx, args []string) error {
	fs := flags("status")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{"status takes only --json"}
	}
	cl, err := client()
	if err != nil {
		return err
	}
	st, err := cl.Status()
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	fmt.Fprintf(c.stdout, "machine %s (fleet %s, enigma %s)\n\n", st.Machine, short(st.Fleet), st.Version)
	if len(st.Problems) > 0 {
		tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "PROBLEMS\tSINCE\tHINT")
		for _, p := range st.Problems {
			fmt.Fprintf(tw, "%s %s\t%s\t%s\n", problemMark(p), p.Message, since(p.Since), p.Hint)
		}
		tw.Flush()
		fmt.Fprintln(c.stdout)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PEER\tLINK\tDETAIL")
	if len(st.Peers) == 0 {
		fmt.Fprintln(tw, "(none)\t\t")
	}
	for _, p := range st.Peers {
		state, detail := "down", p.LastError
		if p.Connected {
			state, detail = "connected", ""
		}
		if p.Target == "" {
			detail = "dialed in"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, state, detail)
	}
	tw.Flush()
	fmt.Fprintln(c.stdout)
	tw = tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SYNCED\tSTATE")
	if len(st.Entries) == 0 {
		fmt.Fprintln(tw, "(nothing yet)\t")
	}
	for _, e := range st.Entries {
		state := "ok"
		switch {
		case e.Missing:
			state = "MISSING LOCALLY: deletes are not propagated"
		case len(e.OverLimit) > 0:
			files := "files"
			if len(e.OverLimit) == 1 {
				files = "file"
			}
			state = fmt.Sprintf("over the %s-file limit, %d new %s not synced (enigma limits --files N raises it)",
				engine.FormatCount(st.Limits.MaxFiles), len(e.OverLimit), files)
		case len(e.Skipped) > 0:
			state = fmt.Sprintf("ok, %d skipped (symlinks or over %s)", len(e.Skipped), engine.FormatSize(st.Limits.MaxFileSize))
		}
		fmt.Fprintf(tw, "%s\t%s\n", entryLabel(e.Path, e.Dir), state)
	}
	tw.Flush()
	if st.Files == 1 {
		fmt.Fprint(c.stdout, "\n1 file")
	} else {
		fmt.Fprintf(c.stdout, "\n%d files", st.Files)
	}
	if len(st.Conflicts) > 0 {
		fmt.Fprintf(c.stdout, ", %d conflict copies:", len(st.Conflicts))
		for _, cf := range st.Conflicts {
			fmt.Fprintf(c.stdout, "\n  ~/%s", cf)
		}
	}
	fmt.Fprintln(c.stdout)
	return nil
}

func entryLabel(rel string, dir bool) string {
	if dir {
		return "~/" + rel + "/"
	}
	return "~/" + rel
}

func tilde(abs string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return abs
	}
	if r, err := filepath.Rel(home, abs); err == nil && !strings.HasPrefix(r, "..") {
		return "~/" + filepath.ToSlash(r)
	}
	return abs
}

// problemMark is "!" for an error, "~" for a warning (SPEC §13.5).
func problemMark(p problems.Problem) string {
	if p.Severity == problems.Error {
		return "!"
	}
	return "~"
}

// since is a problem's age for a human: "just now", "3m", "2h", "1d".
func since(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
