package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/nadavushka-dev/enigma/internal/trash"
)

func init() {
	register(&command{name: "trash", args: "list [--json] | restore <id> | empty", summary: "files removed here by a synced delete (kept 30 days)", run: cmdTrash})
}

func cmdTrash(c *ctx, args []string) error {
	p, err := paths()
	if err != nil {
		return err
	}
	dir := filepath.Join(p.Data, "trash")
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		fs := flags("trash list")
		asJSON := fs.Bool("json", false, "")
		if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
			return usageError{"trash list takes only --json"}
		}
		items, err := trash.List(dir)
		if err != nil {
			return err
		}
		if *asJSON {
			if items == nil {
				items = []trash.Item{}
			}
			enc := json.NewEncoder(c.stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(items)
		}
		if len(items) == 0 {
			fmt.Fprintln(c.stdout, "the trash is empty")
			return nil
		}
		tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tFILE\tSIZE")
		for _, it := range items {
			fmt.Fprintf(tw, "%s\t~/%s\t%d\n", it.ID, it.Path, it.Size)
		}
		tw.Flush()
		fmt.Fprintln(c.stdout, "restore one with `enigma trash restore <id>`")
		return nil
	case "restore":
		if len(args) != 1 {
			return usageError{"trash restore needs exactly one id (see `enigma trash list`)"}
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dst, err := trash.Restore(dir, home, args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "restored %s; if it is synced, it reaches every machine as a normal edit\n", tilde(dst))
		return nil
	case "empty":
		if len(args) != 0 {
			return usageError{"trash empty takes no arguments"}
		}
		n, err := trash.Empty(dir)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "removed %d file(s) from the trash\n", n)
		return nil
	}
	return usageError{"usage: enigma trash list [--json] | restore <id> | empty"}
}
