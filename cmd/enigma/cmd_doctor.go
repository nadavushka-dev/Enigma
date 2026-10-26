package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/group"
	"github.com/nadavushka-dev/enigma/internal/problems"
	"github.com/nadavushka-dev/enigma/internal/proto"
	"github.com/nadavushka-dev/enigma/internal/service"
)

func init() {
	register(&command{name: "doctor", args: "[--json]", summary: "check this machine and the links between all machines", run: cmdDoctor})
}

// doctorMachine is one member as it reports itself (asked over ssh).
type doctorMachine struct {
	Name    string          `json:"name"`
	Version string          `json:"version,omitempty"`
	Error   string          `json:"error,omitempty"` // could not ask it
	Links   map[string]bool `json:"links,omitempty"` // its view: linked to name
	errs    map[string]string
}

type doctorReport struct {
	Checks   []string        `json:"checks"`
	Machines []doctorMachine `json:"machines"`
	Problems []string        `json:"problems"`
	Warnings []string        `json:"warnings,omitempty"` // standing warnings; they do not fail the check
}

func cmdDoctor(c *ctx, args []string) error {
	fs := flags("doctor")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{"doctor takes only --json"}
	}
	rep := doctorReport{Checks: []string{}, Problems: []string{}}
	cl, err := client()
	if err != nil {
		rep.Problems = append(rep.Problems, err.Error())
		return finishDoctor(c, rep, *asJSON)
	}
	p, _ := paths()
	if o, err := service.Defaults(filepath.Join(p.Data, "enigma.log"), filepath.Join(p.Data, "daemon.out")); err == nil {
		if _, err := os.Stat(service.File(o)); err == nil {
			rep.Checks = append(rep.Checks, "service installed: "+service.File(o))
		} else {
			rep.Checks = append(rep.Checks, "no service installed: the daemon was started by hand or in the background, and does not start at login")
		}
	}
	st, err := cl.Status()
	if err != nil {
		rep.Problems = append(rep.Problems, err.Error())
		return finishDoctor(c, rep, *asJSON)
	}
	rep.Checks = append(rep.Checks, "daemon running (enigma "+st.Version+")")
	// The daemon's standing problems (SPEC §13.5) come first: errors fail
	// the check, warnings are only shown.
	for _, p := range st.Problems {
		msg := p.Message
		if p.Hint != "" {
			msg += " (" + p.Hint + ")"
		}
		if p.Severity == problems.Error {
			rep.Problems = append(rep.Problems, msg)
		} else {
			rep.Warnings = append(rep.Warnings, msg)
		}
	}
	ms, err := group.Members(cl)
	if err != nil {
		return err
	}

	// Ask every member for its own view, in parallel.
	rows := make([]doctorMachine, len(ms))
	var wg sync.WaitGroup
	for i, r := range ms {
		if r.ID == st.MachineID {
			rows[i] = fromStatus(r.Name, st)
			continue
		}
		wg.Add(1)
		go func(i int, r proto.MachineRecord) {
			defer wg.Done()
			rows[i] = askMachine(r)
		}(i, r)
	}
	wg.Wait()
	rep.Machines = rows

	// Problems: unreachable machines, pairs linked from neither side,
	// versions that differ.
	versions := map[string][]string{}
	for i, x := range rows {
		if x.Error != "" {
			rep.Problems = append(rep.Problems, fmt.Sprintf("cannot ask %s: %s", x.Name, x.Error))
			continue
		}
		versions[x.Version] = append(versions[x.Version], x.Name)
		for j, y := range rows {
			if i == j || x.Links[y.Name] || y.Links[x.Name] {
				continue
			}
			if y.Error == "" && j < i {
				continue // reported from y's row
			}
			msg := fmt.Sprintf("%s is not linked to %s", x.Name, y.Name)
			if e := x.errs[y.Name]; e != "" {
				msg += ": " + e
			}
			rep.Problems = append(rep.Problems, msg+" (changes still flow through the machines both are linked to)")
		}
	}
	if len(versions) > 1 {
		var parts []string
		for v, names := range versions {
			parts = append(parts, v+": "+strings.Join(names, ", "))
		}
		sort.Strings(parts)
		rep.Problems = append(rep.Problems, "machines run different versions ("+strings.Join(parts, "; ")+"); they upgrade over their links")
	}
	return finishDoctor(c, rep, *asJSON)
}

func fromStatus(name string, st daemon.StatusReport) doctorMachine {
	m := doctorMachine{Name: name, Version: st.Version, Links: map[string]bool{}, errs: map[string]string{}}
	for _, p := range st.Peers {
		m.Links[p.Name] = p.Connected
		if p.LastError != "" {
			m.errs[p.Name] = p.LastError
		}
	}
	return m
}

func askMachine(r proto.MachineRecord) doctorMachine {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, err := group.AskStatus(ctx, r)
	if err != nil {
		return doctorMachine{Name: r.Name, Error: err.Error()}
	}
	return fromStatus(r.Name, st)
}

func finishDoctor(c *ctx, rep doctorReport, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		if len(rep.Machines) > 0 {
			tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
			head := "FROM \\ TO"
			for _, m := range rep.Machines {
				head += "\t" + m.Name
			}
			fmt.Fprintln(tw, head+"\tVERSION")
			for _, x := range rep.Machines {
				row := x.Name
				for _, y := range rep.Machines {
					switch {
					case x.Name == y.Name:
						row += "\t-"
					case x.Error != "":
						row += "\t?"
					case x.Links[y.Name]:
						row += "\tlinked"
					default:
						row += "\tNO"
					}
				}
				v := x.Version
				if x.Error != "" {
					v = "?"
				}
				fmt.Fprintln(tw, row+"\t"+v)
			}
			tw.Flush()
			fmt.Fprintln(c.stdout)
		}
		for _, ch := range rep.Checks {
			fmt.Fprintln(c.stdout, "ok:", ch)
		}
		for _, pr := range rep.Warnings {
			fmt.Fprintln(c.stdout, "warning:", pr)
		}
		for _, pr := range rep.Problems {
			fmt.Fprintln(c.stdout, "problem:", pr)
		}
	}
	if len(rep.Problems) > 0 {
		return fmt.Errorf("%d problem(s) found", len(rep.Problems))
	}
	return nil
}
