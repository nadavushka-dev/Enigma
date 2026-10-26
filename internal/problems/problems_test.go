package problems

import (
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/engine"
)

var t0 = time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC)

func lim() engine.Limits { return engine.Limits{MaxFileSize: 10 << 20, MaxFiles: 2000} }

func keys(ps []Problem) string {
	var ks []string
	for _, p := range ps {
		ks = append(ks, p.Key)
	}
	return strings.Join(ks, " ")
}

func TestD20_NoProblemsWhenAllIsWell(t *testing.T) {
	st := Input{Limits: lim(),
		Peers:   []Peer{{Name: "devbox", Connected: true}},
		Entries: []engine.EntryStatus{{Path: ".claude", Dir: true}}}
	if ps := Derive(st, t0); len(ps) != 0 {
		t.Fatalf("got %v", ps)
	}
}

func TestD20_LinkDownIsAnErrorWithTheSshError(t *testing.T) {
	st := Input{Limits: lim(),
		Peers: []Peer{{Name: "laptop", Dials: true, LastError: "ssh: connection refused"}}}
	ps := Derive(st, t0)
	if len(ps) != 1 || ps[0].Key != "link:laptop" || ps[0].Severity != Error || ps[0].Peer != "laptop" {
		t.Fatalf("got %+v", ps)
	}
	if !strings.Contains(ps[0].Message, "laptop link down") || !strings.Contains(ps[0].Message, "connection refused") {
		t.Fatalf("message: %q", ps[0].Message)
	}
	if !strings.Contains(ps[0].Hint, "enigma doctor") {
		t.Fatalf("hint: %q", ps[0].Hint)
	}
}

func TestD20_FounderWithoutTargetHintsAlias(t *testing.T) {
	// A member that dials us and is not connected: we cannot reach it
	// (SPEC §12: the founder has no target).
	st := Input{Limits: lim(), Peers: []Peer{{Name: "mac"}}}
	ps := Derive(st, t0)
	if len(ps) != 1 || ps[0].Severity != Warning || !strings.Contains(ps[0].Hint, "enigma machine alias mac") {
		t.Fatalf("got %+v", ps)
	}
}

func TestD20_VersionMismatchThatCannotUpgrade(t *testing.T) {
	st := Input{Limits: lim(),
		Peers: []Peer{{Name: "nas", Dials: true, LastError: "link carried an upgrade: cannot upgrade nas from 0.1.0 to 0.2.0: no linux/arm64 binary"}}}
	ps := Derive(st, t0)
	if len(ps) != 1 || ps[0].Key != "upgrade:nas" || ps[0].Severity != Error || !strings.Contains(ps[0].Hint, "make dist") {
		t.Fatalf("got %+v", ps)
	}
}

func TestD20_EntryProblems(t *testing.T) {
	st := Input{Limits: lim(), Entries: []engine.EntryStatus{
		{Path: "notes", Dir: true, Missing: true},
		{Path: ".claude", Dir: true, OverLimit: []string{"a", "b", "c"}},
		{Path: "bin", Dir: true, Skipped: []string{"big.iso", "link"}},
	}}
	ps := Derive(st, t0)
	if got := keys(ps); got != "missing:notes overlimit:.claude skipped:bin" {
		t.Fatalf("keys: %q", got)
	}
	if ps[0].Severity != Error || ps[0].Path != "notes" || !strings.Contains(ps[0].Message, "missing locally") {
		t.Fatalf("missing: %+v", ps[0])
	}
	if ps[1].Severity != Error || !strings.Contains(ps[1].Message, "2,000-file limit, 3 new files not synced") || !strings.Contains(ps[1].Hint, "enigma limits --files") {
		t.Fatalf("overlimit: %+v", ps[1])
	}
	if ps[2].Severity != Warning || !strings.Contains(ps[2].Message, "2 files skipped") || !strings.Contains(ps[2].Message, "10 MB") {
		t.Fatalf("skipped: %+v", ps[2])
	}
}

func TestD20_RecentConflictIsAWarningThatClearsAfterAnHour(t *testing.T) {
	ev := []engine.Event{{Seq: 1, Time: t0.Add(-30 * time.Minute), Kind: engine.EvConflict, Path: "notes/todo.conflict-mac-20261026-133000-1a2b3c.md", Detail: "notes/todo.md"}}
	ps := Derive(Input{Limits: lim(), Events: ev}, t0)
	if len(ps) != 1 || ps[0].Key != "conflict:notes/todo.conflict-mac-20261026-133000-1a2b3c.md" || ps[0].Severity != Warning || ps[0].Path != "notes/todo.conflict-mac-20261026-133000-1a2b3c.md" {
		t.Fatalf("got %+v", ps)
	}
	if ps := Derive(Input{Limits: lim(), Events: ev}, t0.Add(31*time.Minute)); len(ps) != 0 {
		t.Fatalf("not cleared: %+v", ps)
	}
}

func TestD20_RecentErrorEventIsAProblem(t *testing.T) {
	ev := []engine.Event{{Seq: 1, Time: t0.Add(-time.Minute), Kind: engine.EvError, Path: "notes/x", Detail: "cannot write: a folder is in the way"}}
	ps := Derive(Input{Limits: lim(), Events: ev}, t0)
	if len(ps) != 1 || ps[0].Key != "error:1" || ps[0].Severity != Error || ps[0].Path != "notes/x" || !strings.Contains(ps[0].Message, "a folder is in the way") {
		t.Fatalf("got %+v", ps)
	}
}

func TestD20_ErrorsSortBeforeWarnings(t *testing.T) {
	st := Input{Limits: lim(),
		Peers:   []Peer{{Name: "mac"}, {Name: "laptop", Dials: true, LastError: "boom"}},
		Entries: []engine.EntryStatus{{Path: "bin", Dir: true, Skipped: []string{"x"}}}}
	if got := keys(Derive(st, t0)); got != "link:laptop link:mac skipped:bin" {
		t.Fatalf("keys: %q", got)
	}
}

func TestD20_TrackerKeepsSinceWhileAProblemStands(t *testing.T) {
	var tr Tracker
	down := Input{Limits: lim(), Peers: []Peer{{Name: "laptop", Dials: true, LastError: "boom"}}}
	ps := tr.Update(down, t0)
	if len(ps) != 1 || !ps[0].Since.Equal(t0) {
		t.Fatalf("first: %+v", ps)
	}
	ps = tr.Update(down, t0.Add(time.Minute))
	if len(ps) != 1 || !ps[0].Since.Equal(t0) {
		t.Fatalf("second: %+v", ps)
	}
	up := Input{Limits: lim(), Peers: []Peer{{Name: "laptop", Dials: true, Connected: true}}}
	if ps = tr.Update(up, t0.Add(2*time.Minute)); len(ps) != 0 {
		t.Fatalf("resolved: %+v", ps)
	}
	ps = tr.Update(down, t0.Add(3*time.Minute))
	if len(ps) != 1 || !ps[0].Since.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("again: %+v", ps)
	}
}

func TestD20_SummaryLine(t *testing.T) {
	if s := Summary(nil); s != "all good" {
		t.Fatalf("empty: %q", s)
	}
	ps := []Problem{
		{Key: "link:laptop", Severity: Error, Message: "laptop link down (boom)", Since: t0},
		{Key: "skipped:bin", Severity: Warning, Message: "bin: 1 file skipped"},
	}
	if s := Summary(ps); s != "2 problems: laptop link down (boom)" {
		t.Fatalf("got %q", s)
	}
	if s := Summary(ps[1:]); s != "1 problem: bin: 1 file skipped" {
		t.Fatalf("got %q", s)
	}
}
