package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nadavushka-dev/enigma/internal/engine"
)

func init() {
	register(&command{name: "limits", args: "[--file-size SIZE] [--files N] [--json]",
		summary: "show the group's limits, or change them for every machine", run: cmdLimits})
}

func cmdLimits(c *ctx, args []string) error {
	fs := flags("limits")
	size := fs.String("file-size", "", "")
	files := fs.String("files", "", "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{"limits takes only flags"}
	}
	var want engine.Limits
	if *size != "" {
		n, err := parseSize(*size)
		if err != nil {
			return usageError{err.Error()}
		}
		want.MaxFileSize = n
	}
	if *files != "" {
		n, err := strconv.Atoi(*files)
		if err != nil || n <= 0 {
			return usageError{fmt.Sprintf("--files wants a positive number, not %q", *files)}
		}
		want.MaxFiles = n
	}
	cl, err := client()
	if err != nil {
		return err
	}
	var l engine.Limits
	if want == (engine.Limits{}) {
		l, err = cl.Limits()
	} else {
		l, err = cl.SetLimits(want)
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(c.stdout).Encode(l)
	}
	fmt.Fprintln(c.stdout, l)
	if want != (engine.Limits{}) {
		fmt.Fprintln(c.stdout, "set for every machine in the group")
	}
	return nil
}

// parseSize reads a size like 25MB, 500K or 1.5 MB (binary units; a bare
// number is bytes).
func parseSize(s string) (int64, error) {
	t := strings.ToUpper(strings.ReplaceAll(s, " ", ""))
	units := []struct {
		suffix string
		mult   float64
	}{{"GB", 1 << 30}, {"G", 1 << 30}, {"MB", 1 << 20}, {"M", 1 << 20}, {"KB", 1 << 10}, {"K", 1 << 10}, {"B", 1}}
	mult := 1.0
	for _, u := range units {
		if strings.HasSuffix(t, u.suffix) {
			t, mult = strings.TrimSuffix(t, u.suffix), u.mult
			break
		}
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil || v <= 0 {
		return 0, errors.New("--file-size wants a size like 25MB, not " + strconv.Quote(s))
	}
	return int64(v * mult), nil
}
