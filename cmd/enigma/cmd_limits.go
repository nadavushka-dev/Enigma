package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/nadavushka-dev/enigma/internal/engine"
)

func init() {
	register(&command{name: "limits", args: "[--file-size SIZE] [--files N] [--json]",
		summary: "show the fleet's limits, or change them for every machine", run: cmdLimits})
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
		n, err := engine.ParseSize(*size)
		if err != nil {
			return usageError{"--file-size " + err.Error()}
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
		fmt.Fprintln(c.stdout, "set for every machine in the fleet")
	}
	return nil
}
