package main

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/nadavushka-dev/enigma/internal/engine"
)

func init() {
	register(&command{name: "conflicts", args: "[--json]", summary: "list conflict copies (edits that could not be merged)", run: cmdConflicts})
}

type conflictJSON struct {
	Path string `json:"path"`
	engine.ConflictCopy
}

func cmdConflicts(c *ctx, args []string) error {
	fs := flags("conflicts")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{"conflicts takes only --json"}
	}
	cl, err := client()
	if err != nil {
		return err
	}
	st, err := cl.Status()
	if err != nil {
		return err
	}
	out := []conflictJSON{}
	for _, p := range st.Conflicts {
		if cc, ok := engine.ParseConflictName(p); ok {
			out = append(out, conflictJSON{Path: p, ConflictCopy: cc})
		}
	}
	if *asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	if len(out) == 0 {
		fmt.Fprintln(c.stdout, "no conflict copies")
		return nil
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "COPY\tOF\tEDIT BY\tEDITED (UTC)")
	for _, cf := range out {
		fmt.Fprintf(tw, "~/%s\t~/%s\t%s\t%s\n", cf.Path, cf.Original, cf.Machine, cf.Time.Format("2006-01-02 15:04:05"))
	}
	tw.Flush()
	fmt.Fprintln(c.stdout, "each copy holds an edit that lost; keep what you need in the original, then delete the copy")
	return nil
}
