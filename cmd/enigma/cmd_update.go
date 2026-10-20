package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nadavushka-dev/enigma/internal/buildinfo"
	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/release"
)

func init() {
	register(&command{name: "update", args: "[--check]", summary: "move this machine to the latest release (the others follow)", run: cmdUpdate})
}

func cmdUpdate(c *ctx, args []string) error {
	fs := flags("update")
	check := fs.Bool("check", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{"update takes only --check"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	src := release.Default()
	latest, err := src.Latest(ctx)
	if err != nil {
		return err
	}
	cur := buildinfo.Version
	order, ok := buildinfo.Compare(latest, cur)
	switch {
	case !ok:
		return fmt.Errorf("this build (%s) cannot be compared with the latest release %s; install a release with install.sh", cur, latest)
	case order <= 0:
		fmt.Fprintf(c.stdout, "enigma %s is up to date\n", cur)
		return nil
	case *check:
		fmt.Fprintf(c.stdout, "update available: %s -> %s (run `enigma update`)\n", cur, latest)
		return nil
	}

	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "downloading enigma %s for %s\n", latest, buildinfo.Platform())
	bin, err := src.Fetch(ctx, latest, buildinfo.Platform())
	if err != nil {
		return err
	}
	if err := replaceBinary(ctx, self, bin, latest); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "updated to %s (%s)\n", latest, self)

	p, err := paths()
	if err != nil {
		return err
	}
	cl := daemon.NewClient(p)
	if _, err := cl.Status(); err != nil {
		return nil // no daemon running here
	}
	if err := cl.Restart(); err != nil {
		return err
	}
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if st, err := cl.Status(); err == nil && st.Version == latest {
			fmt.Fprintf(c.stdout, "the daemon restarted on %s; the other machines move to it over their links\n", latest)
			return nil
		}
	}
	fmt.Fprintf(c.stdout, "warning: the daemon did not come back on %s; is it running another binary? see `enigma status`\n", latest)
	return nil
}

// replaceBinary swaps path for bin atomically, once bin says it is version.
func replaceBinary(ctx context.Context, path string, bin []byte, version string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".enigma-update-*")
	if err != nil {
		return fmt.Errorf("cannot replace %s: %v (reinstall with install.sh, or run with sudo)", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, tmp.Name(), "version").Output()
	if f := strings.Fields(string(out)); err != nil || len(f) < 2 || f[1] != version {
		return fmt.Errorf("the downloaded binary is not %s (it says %q)", version, strings.TrimSpace(string(out)))
	}
	return os.Rename(tmp.Name(), path)
}
