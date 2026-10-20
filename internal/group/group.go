// Package group runs the multi-step operations on the group's machines
// over ssh (SPEC §11): adding a machine, removing one, asking a member for
// its status. The CLI and the TUI both use it; steps are written to an
// io.Writer as they happen.
package group

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nadavushka-dev/enigma/internal/buildinfo"
	"github.com/nadavushka-dev/enigma/internal/config"
	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/link"
	"github.com/nadavushka-dev/enigma/internal/proto"
	"github.com/nadavushka-dev/enigma/internal/release"
	"github.com/nadavushka-dev/enigma/internal/sshconfig"
)

// RemoteBin is where Add installs enigma (SPEC §11 step 3).
const RemoteBin = "~/.local/bin/enigma"

// JoinRequest is what Add sends to `enigma join` on the new machine.
type JoinRequest struct {
	Identity config.Identity       `json:"identity"`
	Members  []proto.MachineRecord `json:"members"`
}

// Members are the machine records not removed.
func Members(cl *daemon.Client) ([]proto.MachineRecord, error) {
	all, err := cl.Machines()
	if err != nil {
		return nil, err
	}
	var out []proto.MachineRecord
	for _, r := range all {
		if !r.Removed {
			out = append(out, r)
		}
	}
	return out, nil
}

func ByName(rs []proto.MachineRecord, name string) (proto.MachineRecord, bool) {
	for _, r := range rs {
		if r.Name == name {
			return r, true
		}
	}
	return proto.MachineRecord{}, false
}

func PeerByName(ps []daemon.PeerReport, name string) (daemon.PeerReport, bool) {
	for _, p := range ps {
		if p.Name == name {
			return p, true
		}
	}
	return daemon.PeerReport{}, false
}

// Add installs enigma on the machine at ssh alias and makes it a member
// named name (default: the alias), printing every step to out (D9).
func Add(ctx context.Context, cl *daemon.Client, p config.Paths, alias, name string, out io.Writer) error {
	if name == "" {
		name = alias
	}
	id, err := config.LoadIdentity(p)
	if err != nil {
		return err
	}
	ms, err := Members(cl)
	if err != nil {
		return err
	}
	for _, r := range ms {
		if r.Name == name {
			return fmt.Errorf("a machine named %q is already in the group (use --name)", name)
		}
		if r.Alias == alias {
			return fmt.Errorf("%s is already in the group, as %s", alias, r.Name)
		}
	}
	step := func(n int, what, cmd string) {
		fmt.Fprintf(out, "%d. %s\n   $ %s\n", n, what, cmd)
	}

	step(1, "detect the platform", "ssh "+alias+" uname -sm")
	res, err := link.Shell(ctx, alias, "uname -sm", nil)
	if err != nil {
		return err
	}
	platform := buildinfo.PlatformFromUname(string(res))
	if platform == "" {
		return fmt.Errorf("%s runs %q: enigma supports macOS and Linux on amd64 and arm64", alias, strings.TrimSpace(string(res)))
	}
	fmt.Fprintf(out, "   %s\n", platform)

	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	if err != nil {
		return err
	}
	binary, src, err := release.BinaryFor(ctx, release.Default(), p.Data, self, buildinfo.Version, buildinfo.Platform(), platform)
	if err != nil {
		return err
	}
	step(2, "copy enigma "+buildinfo.Version+" ("+src+")", "ssh "+alias+" 'mkdir -p ~/.local/bin && cat > ~/.local/bin/.enigma-new && mv -f ~/.local/bin/.enigma-new "+RemoteBin+"'")
	install := `mkdir -p ~/.local/bin && t=~/.local/bin/.enigma-new.$$ && cat > "$t" && chmod 755 "$t" && mv -f "$t" ` + RemoteBin + ` && ` + RemoteBin + ` version`
	res, err = link.Shell(ctx, alias, install, bytes.NewReader(binary))
	if err != nil {
		return err
	}
	if f := strings.Fields(string(res)); len(f) < 2 || f[1] != buildinfo.Version {
		return fmt.Errorf("the installed binary on %s says %q, not %s", alias, strings.TrimSpace(string(res)), buildinfo.Version)
	}

	step(3, "record how to reach it", "ssh -G "+alias)
	target, err := link.Resolve(ctx, alias)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "   %s\n", target)
	rec := proto.MachineRecord{ID: config.NewID(), Name: name, Alias: alias, Target: target, Bin: RemoteBin}

	step(4, "join the group (the group is sent on stdin)", "ssh "+alias+" "+RemoteBin+" join")
	req, _ := json.Marshal(JoinRequest{Identity: config.Identity{MachineID: rec.ID, GroupID: id.GroupID, Name: name}, Members: append(ms, rec)})
	if _, err := link.Shell(ctx, alias, RemoteBin+" join", bytes.NewReader(req)); err != nil {
		return err
	}

	step(5, "record "+name+" in the group; every machine links to it", "(this machine's daemon)")
	if err := cl.SetMachine(rec); err != nil {
		return err
	}

	step(6, "start its daemon", "ssh "+alias+" "+RemoteBin+" service install")
	res, err = link.Shell(ctx, alias, RemoteBin+" service install", nil)
	if err != nil {
		return err
	}
	for _, l := range strings.Split(strings.TrimSpace(string(res)), "\n") {
		fmt.Fprintf(out, "   %s\n", l)
	}

	step(7, "check its links", "ssh "+alias+" "+RemoteBin+" status --json")
	missing := 0
	for _, pr := range remoteLinks(ctx, alias, id.Name) {
		switch {
		case pr.Connected:
			fmt.Fprintf(out, "   connected to %s\n", pr.Name)
		case pr.Target == "":
			fmt.Fprintf(out, "   %s: not linked yet (it has no address for %s; %s dials it)\n", pr.Name, pr.Name, pr.Name)
		default:
			missing++
			fmt.Fprintf(out, "   warning: not connected to %s: %s\n", pr.Name, pr.LastError)
		}
	}
	if missing > 0 {
		fmt.Fprintln(out, "   changes still reach it through the machines it is linked to")
	}
	fmt.Fprintf(out, "done: %s is in the group\n", name)
	return nil
}

// remoteLinks waits until the new machine is linked to this one (a few
// seconds more for the others) and returns its view of its peers.
func remoteLinks(ctx context.Context, alias, self string) []daemon.PeerReport {
	var st daemon.StatusReport
	read := func() bool {
		out, err := link.Shell(ctx, alias, RemoteBin+" status --json", nil)
		return err == nil && json.Unmarshal(out, &st) == nil
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if read() {
			if pr, ok := PeerByName(st.Peers, self); ok && pr.Connected {
				time.Sleep(2 * time.Second) // let the other links come up
				read()
				return st.Peers
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return st.Peers
}

// Remove takes member name out of the group and, if it is reachable, tells
// it to leave (and with uninstall, to remove enigma).
func Remove(ctx context.Context, cl *daemon.Client, name string, uninstall bool, out io.Writer) error {
	ms, err := Members(cl)
	if err != nil {
		return err
	}
	r, ok := ByName(ms, name)
	if !ok {
		return fmt.Errorf("no machine named %q in the group (see `enigma machine list`)", name)
	}
	st, err := cl.Status()
	if err != nil {
		return err
	}
	if r.ID == st.MachineID {
		return fmt.Errorf("%s is this machine: remove it from another member", name)
	}
	r.Removed = true
	if err := cl.SetMachine(r); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %s from the group; no machine links to it anymore\n", name)

	leave := r.Bin + " leave"
	if r.Bin == "" {
		leave = "enigma leave"
	}
	if uninstall {
		leave += " --uninstall"
	}
	var lastErr error
	for _, t := range TargetsOf(r) {
		fmt.Fprintf(out, "telling it to leave\n   $ ssh %s %s\n", t, leave)
		res, err := link.Shell(ctx, t, leave, nil)
		if err == nil {
			fmt.Fprintf(out, "   %s: %s\n", name, strings.TrimSpace(string(res)))
			return nil
		}
		lastErr = err
	}
	if lastErr != nil {
		fmt.Fprintf(out, "warning: could not reach %s (%v); it keeps running there until you run `enigma leave` on it\n", name, lastErr)
	} else {
		fmt.Fprintf(out, "warning: no address for %s; run `enigma leave` on it\n", name)
	}
	return nil
}

// TargetsOf is how this machine reaches r, as its daemon would (SPEC §5).
func TargetsOf(r proto.MachineRecord) []string {
	home, _ := os.UserHomeDir()
	overrides := map[string]string{}
	if p, err := config.Default(); err == nil {
		if l, err := config.LoadLocal(p); err == nil {
			overrides = l.Aliases
		}
	}
	return daemon.Targets(r, overrides, func(a string) bool { return sshconfig.Has(home, a) })
}

// AskStatus asks member r for its status over ssh.
func AskStatus(ctx context.Context, r proto.MachineRecord) (daemon.StatusReport, error) {
	bin := r.Bin
	if bin == "" {
		bin = "enigma"
	}
	targets := TargetsOf(r)
	if len(targets) == 0 {
		return daemon.StatusReport{}, fmt.Errorf("no address for %s on this machine (set one with `enigma machine alias`)", r.Name)
	}
	var lastErr error
	for _, t := range targets {
		out, err := link.Shell(ctx, t, bin+" status --json", nil)
		if err != nil {
			lastErr = err
			continue
		}
		var st daemon.StatusReport
		if err := json.Unmarshal(out, &st); err != nil {
			lastErr = fmt.Errorf("its status: %v", err)
			continue
		}
		return st, nil
	}
	return daemon.StatusReport{}, lastErr
}
