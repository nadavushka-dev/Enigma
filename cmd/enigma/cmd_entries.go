package main

import (
	"bufio"
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
	"github.com/nadavushka-dev/enigma/internal/proto"
)

func init() {
	register(&command{name: "add", args: "[--group G] [--ignore GLOB]... [--on-existing keep-both|mine-wins|cancel] [--dry-run] [--json] [path]", summary: "sync a file or folder on the machines of a group (default main; no path: pick with fzf)", run: cmdAdd})
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
	Preview(abs string, ignore []string, group string) (engine.AdoptionReport, error)
	Add(abs string, ignore []string, group string) error
	AddWithPolicy(abs string, ignore []string, group string, policy proto.FirstAddPolicy, policies map[string]proto.FirstAddPolicy) error
}

// The seams of `add` with no path (SPEC §12.4) and its prompt, replaced in tests.
var (
	entryAdder          = func() (adder, error) { return client() }
	pickerIsTerminal    = func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) }
	addPromptIsTerminal = func(c *ctx) bool {
		f, ok := c.stdin.(*os.File)
		return ok && isTerminal(f)
	}
	haveFzf = func() bool { _, err := exec.LookPath("fzf"); return err == nil }
	runFzf  = fzfMulti
)

var errNoPicker = errors.New("give a path, or install fzf to pick one")

// firstAddChoice is the user's answer to a first-add difference (D22).
type firstAddChoice string

const (
	firstAddUnset    firstAddChoice = ""
	firstAddKeepBoth firstAddChoice = "keep-both"
	firstAddMineWins firstAddChoice = "mine-wins"
	firstAddCancel   firstAddChoice = "cancel"
)

func parseFirstAddChoice(s string) (firstAddChoice, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return firstAddUnset, nil
	case "keep-both", "keep", "k":
		return firstAddKeepBoth, nil
	case "mine-wins", "mine", "replace", "m":
		return firstAddMineWins, nil
	case "cancel", "c":
		return firstAddCancel, nil
	}
	return "", fmt.Errorf("--on-existing wants keep-both, mine-wins or cancel, not %q", s)
}

func (ch firstAddChoice) policy() proto.FirstAddPolicy {
	if ch == firstAddMineWins {
		return proto.FirstAddReplace
	}
	return proto.FirstAddKeepBoth
}

type addResult struct {
	Path   string                `json:"path"`
	Action string                `json:"action"` // added, cancelled, dry-run
	Policy string                `json:"policy,omitempty"`
	Report engine.AdoptionReport `json:"report"`
}

func cmdAdd(c *ctx, args []string) error {
	fs := flags("add")
	var ignore multi
	fs.Var(&ignore, "ignore", "")
	group := fs.String("group", "", "")
	onExisting := fs.String("on-existing", "", "")
	dryRun := fs.Bool("dry-run", false, "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 1 {
		return usageError{"add takes one path, or none to pick with fzf"}
	}
	choice, err := parseFirstAddChoice(*onExisting)
	if err != nil {
		return usageError{err.Error()}
	}
	groupID, groupName := "", ""
	if *group != "" && *group != "main" {
		cl, err := client()
		if err != nil {
			return err
		}
		g, err := groupNamed(cl, *group)
		if err != nil {
			return err
		}
		groupID, groupName = g.ID, g.Name
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
	var input *bufio.Reader
	if !*asJSON && addPromptIsTerminal(c) {
		input = bufio.NewReader(c.stdin)
	}
	var results []addResult
	for _, p := range picks {
		abs, err := absPath(p)
		if err != nil {
			return err
		}
		rep, err := cl.Preview(abs, ignore, groupID)
		if err != nil {
			return err
		}
		res := addResult{Path: tilde(abs) + trailingSlash(p), Report: rep}
		if *dryRun {
			res.Action = "dry-run"
			if !*asJSON {
				printAdoption(c, rep)
			}
			results = append(results, res)
			continue
		}
		if !rep.Local.Exists {
			return fmt.Errorf("%s does not exist on this machine; nothing was added", tilde(abs))
		}
		if name, group, ok := rep.SyncedElsewhere(); ok {
			return fmt.Errorf("%s is already synced on %s in group %s; remove it there first or wait for the fleet state to catch up", tilde(abs), name, group)
		}
		if why := rep.Blocked(); why != "" {
			return fmt.Errorf("cannot add %s: %s", tilde(abs), why)
		}
		if !*asJSON && (rep.NeedsDecision() || anyPeerHas(rep)) {
			printAdoption(c, rep)
		}
		chosen := choice
		if rep.NeedsDecision() && chosen == firstAddUnset {
			if *asJSON || input == nil {
				return fmt.Errorf("%s already exists on another machine with different content; pass --on-existing=keep-both|mine-wins|cancel", tilde(abs))
			}
			if chosen, err = askFirstAdd(c, input); err != nil {
				return err
			}
		}
		if chosen == firstAddCancel {
			res.Action = "cancelled"
			if !*asJSON {
				fmt.Fprintf(c.stdout, "cancelled; %s was not added\n", tilde(abs))
			}
			results = append(results, res)
			continue
		}
		policy := chosen.policy()
		if err := cl.AddWithPolicy(abs, ignore, groupID, policy, nil); err != nil {
			return err
		}
		res.Action, res.Policy = "added", string(policy)
		results = append(results, res)
		if *asJSON {
			continue
		}
		if groupName != "" {
			fmt.Fprintf(c.stdout, "syncing %s in group %s\n", tilde(abs)+trailingSlash(p), groupName)
		} else {
			fmt.Fprintf(c.stdout, "syncing %s on every machine\n", tilde(abs)+trailingSlash(p))
		}
		switch {
		case policy == proto.FirstAddKeepBoth && rep.NeedsDecision():
			fmt.Fprintln(c.stdout, "kept both copies; the other version is next to it as a .conflict- file")
		case policy == proto.FirstAddReplace && rep.Summary.Unknown > 0:
			fmt.Fprintf(c.stdout, "note: %s offline; this machine wins will replace its copy (backed up) when it reconnects\n", pluralMachines(rep.Summary.Unknown))
		}
	}
	if !*asJSON {
		return nil
	}
	enc := json.NewEncoder(c.stdout)
	enc.SetIndent("", "  ")
	if len(results) == 1 {
		return enc.Encode(results[0])
	}
	return enc.Encode(results)
}

func anyPeerHas(r engine.AdoptionReport) bool {
	for _, m := range r.Machines {
		if !m.Local && m.Exists {
			return true
		}
	}
	return false
}

func pluralMachines(n int) string {
	if n == 1 {
		return "1 machine is"
	}
	return fmt.Sprintf("%d machines are", n)
}

func askFirstAdd(c *ctx, in *bufio.Reader) (firstAddChoice, error) {
	for {
		fmt.Fprint(c.stdout, "\n[k]eep both  [m]ine wins  [c]ancel: ")
		line, err := in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "k", "keep-both", "keep both":
			return firstAddKeepBoth, nil
		case "m", "mine-wins", "mine wins":
			return firstAddMineWins, nil
		case "c", "cancel", "":
			return firstAddCancel, nil
		}
		if err != nil {
			return firstAddCancel, nil
		}
		fmt.Fprintln(c.stdout, "please answer k, m or c")
	}
}

// printAdoption shows which machines have the path and what differs.
func printAdoption(c *ctx, r engine.AdoptionReport) {
	if len(r.Machines) <= 1 {
		fmt.Fprintf(c.stdout, "%s: only on this machine\n", "~/"+r.Path)
		return
	}
	fmt.Fprintf(c.stdout, "%s: what each machine has\n", "~/"+r.Path)
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  MACHINE\tSTATE")
	for _, m := range r.Machines {
		fmt.Fprintf(tw, "  %s\t%s\n", machineName(m), machineState(r, m))
	}
	tw.Flush()
	if r.Summary.Different > 0 {
		fmt.Fprintf(c.stdout, "  %d differing %s\n", r.Summary.Different, plural(r.Summary.Different, "file", "files"))
	}
	if r.Summary.OnlyThere > 0 {
		fmt.Fprintf(c.stdout, "  %d %s only on other machines; kept and spread\n", r.Summary.OnlyThere, plural(r.Summary.OnlyThere, "file", "files"))
	}
	shown := 0
	for _, f := range r.Files {
		if f.State != engine.AdoptionDifferent {
			continue
		}
		label := "~/" + r.Path
		if f.Path != "" {
			label += "/" + f.Path
		}
		fmt.Fprintf(c.stdout, "    differs: %s (%s)\n", label, strings.Join(f.Machines, ", "))
		if shown++; shown == 5 && r.Summary.Different > 5 {
			fmt.Fprintf(c.stdout, "    and %d more\n", r.Summary.Different-5)
			break
		}
	}
	for _, m := range r.Machines {
		if m.Entry {
			fmt.Fprintf(c.stdout, "  note: ~/%s is already synced on %s in group %s\n", r.Path, m.Name, m.EntryGroupLabel())
		}
	}
	if r.Summary.Unknown > 0 {
		fmt.Fprintf(c.stdout, "  note: %s not checked; the choice applies when it reconnects\n", pluralMachines(r.Summary.Unknown))
	}
	if r.Summary.Truncated > 0 {
		fmt.Fprintf(c.stdout, "  note: %s with too many files to compare; only keep-both is fully safe\n", pluralMachines(r.Summary.Truncated))
	}
}

func machineName(m engine.ProbeMachine) string {
	if m.Local {
		return m.Name + " (here)"
	}
	return m.Name
}

func machineState(r engine.AdoptionReport, m engine.ProbeMachine) string {
	switch {
	case m.Error != "":
		return m.Error
	case !m.Exists:
		return "does not have it"
	case m.Entry:
		return "already synced in group " + m.EntryGroupLabel()
	case m.Truncated:
		return "too many files to compare"
	case r.Dir:
		n := len(m.Files)
		state := fmt.Sprintf("%d %s", n, plural(n, "file", "files"))
		if d, o := peerCounts(r, m.Name); d > 0 || o > 0 {
			state += fmt.Sprintf(" · %d differ", d)
			if o > 0 {
				state += fmt.Sprintf(", %d only there", o)
			}
		}
		return state
	case len(m.Files) == 1:
		f := m.Files[0]
		same := len(r.Local.Files) == 1 && r.Local.Files[0].Hash == f.Hash && r.Local.Files[0].Exec == f.Exec
		when := time.Unix(0, f.Mtime).Format("2006-01-02 15:04")
		if same {
			return fmt.Sprintf("same · %s · %s", engine.FormatSize(f.Size), when)
		}
		return fmt.Sprintf("different · %s · %s", engine.FormatSize(f.Size), when)
	}
	return "unknown"
}

// peerCounts counts the files that differ on, or exist only on, one peer.
func peerCounts(r engine.AdoptionReport, name string) (different, only int) {
	for _, f := range r.Files {
		for _, m := range f.Machines {
			if m != name {
				continue
			}
			switch f.State {
			case engine.AdoptionDifferent:
				different++
			case engine.AdoptionOnlyThere:
				only++
			}
		}
	}
	return
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
	gs, err := cl.Groups()
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, g := range gs {
		names[g.ID] = g.Name
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	for _, e := range st.Entries {
		note := ""
		if e.Frozen {
			note = "(not synced here: this machine is not in " + names[e.Group] + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", entryLabel(e.Path, e.Dir), names[e.Group], note)
	}
	return tw.Flush()
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
