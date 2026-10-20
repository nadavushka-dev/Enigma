package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/nadavushka-dev/enigma/internal/config"
	"github.com/nadavushka-dev/enigma/internal/daemon"
)

func init() {
	register(&command{name: "add", args: "[--ignore GLOB]... <path>", summary: "sync a file or folder on every machine", run: cmdAdd})
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

func cmdAdd(c *ctx, args []string) error {
	fs := flags("add")
	var ignore multi
	fs.Var(&ignore, "ignore", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return usageError{"add needs exactly one path"}
	}
	abs, err := absPath(fs.Arg(0))
	if err != nil {
		return err
	}
	cl, err := client()
	if err != nil {
		return err
	}
	if err := cl.Add(abs, ignore); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "syncing %s on every machine\n", tilde(abs))
	return nil
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
	fmt.Fprintf(c.stdout, "machine %s (group %s)\n\n", st.Machine, short(st.Group))
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
		if e.Missing {
			state = "MISSING LOCALLY: deletes are not propagated"
		} else if len(e.Skipped) > 0 {
			state = fmt.Sprintf("ok, %d skipped (symlinks or too large)", len(e.Skipped))
		}
		fmt.Fprintf(tw, "%s\t%s\n", entryLabel(e.Path, e.Dir), state)
	}
	tw.Flush()
	fmt.Fprintf(c.stdout, "\n%d files", st.Files)
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
