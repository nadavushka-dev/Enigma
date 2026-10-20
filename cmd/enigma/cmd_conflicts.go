package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

func init() {
	register(&command{name: "conflicts", args: "[--local] [--json]", summary: "list conflict copies on every machine (edits that could not be merged)", run: cmdConflicts})
}

type conflictJSON struct {
	Path string `json:"path"`
	engine.ConflictCopy
	On []string `json:"on"` // machines that have this copy
}

func cmdConflicts(c *ctx, args []string) error {
	fs := flags("conflicts")
	asJSON := fs.Bool("json", false, "")
	local := fs.Bool("local", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{"conflicts takes only --local and --json"}
	}
	cl, err := client()
	if err != nil {
		return err
	}
	st, err := cl.Status()
	if err != nil {
		return err
	}
	reports := map[string]daemon.StatusReport{st.Machine: st}
	if !*local {
		// Copies beside single-file entries exist only on the machines that
		// resolved the conflict (SPEC §7): ask every member.
		ms, err := members(cl)
		if err != nil {
			return err
		}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, r := range ms {
			if r.ID == st.MachineID {
				continue
			}
			wg.Add(1)
			go func(r proto.MachineRecord) {
				defer wg.Done()
				rs, err := askStatus(r)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					fmt.Fprintf(c.stderr, "warning: could not ask %s: %v\n", r.Name, err)
					return
				}
				reports[r.Name] = rs
			}(r)
		}
		wg.Wait()
	}
	byPath := map[string]*conflictJSON{}
	for machine, rs := range reports {
		for _, p := range rs.Conflicts {
			cc, ok := engine.ParseConflictName(p)
			if !ok {
				continue
			}
			e := byPath[p]
			if e == nil {
				e = &conflictJSON{Path: p, ConflictCopy: cc}
				byPath[p] = e
			}
			e.On = append(e.On, machine)
		}
	}
	out := []conflictJSON{}
	for _, e := range byPath {
		sort.Strings(e.On)
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
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
	fmt.Fprintln(tw, "COPY\tOF\tEDIT BY\tEDITED (UTC)\tON")
	for _, cf := range out {
		fmt.Fprintf(tw, "~/%s\t~/%s\t%s\t%s\t%s\n", cf.Path, cf.Original, cf.Machine, cf.Time.Format("2006-01-02 15:04:05"), strings.Join(cf.On, ", "))
	}
	tw.Flush()
	fmt.Fprintln(c.stdout, "each copy holds an edit that lost; keep what you need in the original, then delete the copy (on every machine listed)")
	return nil
}
