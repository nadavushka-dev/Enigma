package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nadavushka-dev/enigma/internal/buildinfo"
)

func runCLI(args ...string) (string, string, int) {
	var out, errb bytes.Buffer
	code := run(args, &ctx{stdout: &out, stderr: &errb, stdin: strings.NewReader("")})
	return out.String(), errb.String(), code
}

func TestDashVPrintsTheVersion(t *testing.T) {
	for _, arg := range []string{"-v", "--version"} {
		out, _, code := runCLI(arg)
		if code != 0 || !strings.Contains(out, "enigma "+buildinfo.Version) || !strings.Contains(out, buildinfo.Platform()) {
			t.Fatalf("%s: exit %d, output %q", arg, code, out)
		}
	}
}

func TestVersionFlagRejectsArguments(t *testing.T) {
	_, errb, code := runCLI("-v", "extra")
	if code != 2 || !strings.Contains(errb, "takes no arguments") {
		t.Fatalf("exit %d: %q", code, errb)
	}
}

func TestTopLevelHelp(t *testing.T) {
	out, _, code := runCLI("-h")
	if code != 0 || !strings.Contains(out, "Usage:") || !strings.Contains(out, "enigma add") {
		t.Fatalf("exit %d: %q", code, out)
	}
}

func TestCommandHelp(t *testing.T) {
	for _, args := range [][]string{
		{"add", "-h"},
		{"add", "--help"},
		{"add", "--group", "pair", "-h"},
		{"help", "add"},
	} {
		out, _, code := runCLI(args...)
		if code != 0 || !strings.Contains(out, "usage: enigma add") || !strings.Contains(out, "sync a file or folder") {
			t.Fatalf("%v: exit %d: %q", args, code, out)
		}
	}
}

func TestCommandHelpDoesNotRunTheCommand(t *testing.T) {
	out, errb, code := runCLI("add", "-h")
	if code != 0 || strings.Contains(out, "daemon") || strings.Contains(errb, "daemon") {
		t.Fatalf("exit %d: out %q err %q", code, out, errb)
	}
}

func TestUnknownCommandStillFails(t *testing.T) {
	_, errb, code := runCLI("nope", "-h")
	if code != 2 || !strings.Contains(errb, "unknown command") {
		t.Fatalf("exit %d: %q", code, errb)
	}
}
