// Package sshconfig lists the hosts in the user's ssh config, the machines
// they can pick from (SPEC §5, §12.2). It reads Host lines only; ssh itself
// resolves everything else (`ssh -G`).
package sshconfig

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Hosts returns the concrete host aliases in ~/.ssh/config and its
// Includes, sorted. Patterns (*, ?, !negations) are skipped.
func Hosts(home string) ([]string, error) {
	set := map[string]bool{}
	seen := map[string]bool{}
	if err := read(home, filepath.Join(home, ".ssh", "config"), set, seen); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out, nil
}

// Has reports whether alias is a host in the user's ssh config.
func Has(home, alias string) bool {
	hs, _ := Hosts(home)
	for _, h := range hs {
		if h == alias {
			return true
		}
	}
	return false
}

func read(home, p string, set, seen map[string]bool) error {
	if seen[p] {
		return nil
	}
	seen[p] = true
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, args := split(sc.Text())
		switch strings.ToLower(key) {
		case "host":
			for _, h := range args {
				if !strings.ContainsAny(h, "*?!") {
					set[h] = true
				}
			}
		case "include":
			for _, pat := range args {
				if strings.HasPrefix(pat, "~/") {
					pat = filepath.Join(home, pat[2:])
				} else if !filepath.IsAbs(pat) {
					pat = filepath.Join(home, ".ssh", pat)
				}
				matches, _ := filepath.Glob(pat)
				sort.Strings(matches)
				for _, m := range matches {
					if err := read(home, m, set, seen); err != nil {
						return err
					}
				}
			}
		}
	}
	return sc.Err()
}

// split parses `Keyword args...` or `Keyword=args`, with "quoted" args.
func split(line string) (string, []string) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return "", nil
	}
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return line, nil
	}
	key, rest := line[:i], strings.TrimLeft(line[i:], " \t")
	rest = strings.TrimLeft(strings.TrimPrefix(rest, "="), " \t")
	var args []string
	for rest != "" {
		if rest[0] == '"' {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				args = append(args, rest[1:])
				break
			}
			args = append(args, rest[1:1+end])
			rest = strings.TrimLeft(rest[2+end:], " \t")
			continue
		}
		j := strings.IndexAny(rest, " \t")
		if j < 0 {
			args = append(args, rest)
			break
		}
		args = append(args, rest[:j])
		rest = strings.TrimLeft(rest[j:], " \t")
	}
	return key, args
}
