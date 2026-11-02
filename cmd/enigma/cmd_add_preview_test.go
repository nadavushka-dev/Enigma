package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

// differingPreview is the usual two-machine disagreement for ~/f.txt.
func differingPreview(string) engine.AdoptionReport {
	local := []proto.ProbeFile{{Path: "", Hash: "a", Size: 4, Mtime: 1}}
	peer := []proto.ProbeFile{{Path: "", Hash: "b", Size: 5, Mtime: 2}}
	return engine.AdoptionReport{
		Path: "f.txt", Group: proto.MainGroup, GroupName: "main",
		Local: engine.ProbeMachine{Name: "mac", Local: true, Connected: true, Exists: true, Files: local},
		Machines: []engine.ProbeMachine{
			{Name: "mac", Local: true, Connected: true, Exists: true, Files: local},
			{Name: "ubuntu2", Connected: true, Exists: true, Files: peer},
		},
		Files:   []engine.AdoptionFile{{Path: "", State: engine.AdoptionDifferent, Machines: []string{"ubuntu2"}}},
		Summary: engine.AdoptionSummary{Files: 1, Different: 1},
	}
}

func writeTestFile(t *testing.T, home, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, name), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAddDifferenceWithoutATerminalNamesTheFlag(t *testing.T) {
	fa, _, home := pickWorld(t, false, false, nil)
	fa.preview = differingPreview
	writeTestFile(t, home, "f.txt")
	out, code := runAdd("~/f.txt")
	if code != 1 || !strings.Contains(out, "--on-existing=keep-both|mine-wins|cancel") {
		t.Fatalf("exit %d: %q", code, out)
	}
	if len(fa.adds) != 0 {
		t.Fatalf("added without a choice: %v", fa.adds)
	}
}

func TestAddOnExistingKeepBoth(t *testing.T) {
	fa, _, home := pickWorld(t, false, false, nil)
	fa.preview = differingPreview
	writeTestFile(t, home, "f.txt")
	out, code := runAdd("--on-existing", "keep-both", "~/f.txt")
	if code != 0 || len(fa.policies) != 1 || fa.policies[0] != string(proto.FirstAddKeepBoth) {
		t.Fatalf("exit %d, policies %v: %q", code, fa.policies, out)
	}
	if !strings.Contains(out, "ubuntu2") || !strings.Contains(out, "kept both copies") {
		t.Fatalf("the report or result is missing: %q", out)
	}
}

func TestAddOnExistingMineWins(t *testing.T) {
	fa, _, home := pickWorld(t, false, false, nil)
	fa.preview = differingPreview
	writeTestFile(t, home, "f.txt")
	out, code := runAdd("--on-existing=mine-wins", "~/f.txt")
	if code != 0 || len(fa.policies) != 1 || fa.policies[0] != string(proto.FirstAddReplace) {
		t.Fatalf("exit %d, policies %v: %q", code, fa.policies, out)
	}
}

func TestAddOnExistingCancelAddsNothing(t *testing.T) {
	fa, _, home := pickWorld(t, false, false, nil)
	fa.preview = differingPreview
	writeTestFile(t, home, "f.txt")
	out, code := runAdd("--on-existing=cancel", "~/f.txt")
	if code != 0 || len(fa.adds) != 0 || !strings.Contains(out, "cancelled") {
		t.Fatalf("exit %d adds %v: %q", code, fa.adds, out)
	}
}

func TestAddPromptKeepsBoth(t *testing.T) {
	fa, _, home := pickWorld(t, true, false, nil)
	fa.preview = differingPreview
	writeTestFile(t, home, "f.txt")
	out, code := runAddInput("k\n", "~/f.txt")
	if code != 0 || len(fa.policies) != 1 || fa.policies[0] != string(proto.FirstAddKeepBoth) {
		t.Fatalf("exit %d policies %v: %q", code, fa.policies, out)
	}
	if !strings.Contains(out, "[k]eep both") || !strings.Contains(out, "kept both copies") {
		t.Fatalf("prompt missing: %q", out)
	}
}

func TestAddPromptEmptyCancels(t *testing.T) {
	fa, _, home := pickWorld(t, true, false, nil)
	fa.preview = differingPreview
	writeTestFile(t, home, "f.txt")
	out, code := runAddInput("\n", "~/f.txt")
	if code != 0 || len(fa.adds) != 0 || !strings.Contains(out, "cancelled") {
		t.Fatalf("exit %d adds %v: %q", code, fa.adds, out)
	}
}

func TestAddDryRunJSONShowsTheReportAndAddsNothing(t *testing.T) {
	fa, _, home := pickWorld(t, false, false, nil)
	fa.preview = differingPreview
	writeTestFile(t, home, "f.txt")
	out, code := runAdd("--dry-run", "--json", "~/f.txt")
	if code != 0 || len(fa.adds) != 0 {
		t.Fatalf("exit %d adds %v: %q", code, fa.adds, out)
	}
	var res addResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if res.Action != "dry-run" || res.Report.Summary.Different != 1 || res.Report.Machines[1].Name != "ubuntu2" {
		t.Fatalf("result: %+v", res)
	}
}
