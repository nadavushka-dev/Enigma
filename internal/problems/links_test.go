package problems

import (
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/engine"
)

func TestD23_LinkProblems(t *testing.T) {
	links := []engine.LinkStatus{
		{Path: "s/ok", Target: "~/r/ok", State: engine.LinkSynced},
		{Path: "s/out", Target: "/etc", State: engine.LinkSkipped, Reason: "target outside your home folder", Since: t0},
		{Path: "s/new", Target: "~/r/new", State: engine.LinkPending, Since: t0.Add(-10 * time.Second)},
		{Path: "s/old", Target: "~/r/old", State: engine.LinkPending, Since: t0.Add(-2 * time.Minute)},
		{Path: "s/dir", Target: "~/r/dir", State: engine.LinkBlocked, Reason: "a folder is in the way here"},
		{Path: "s/gone", Target: "~/r/gone", State: engine.LinkDangling, Reason: "its target is not here"},
	}
	st := Input{Limits: lim(), Entries: []engine.EntryStatus{{Path: "s", Dir: true, LinksOn: true, Links: links,
		Skipped: []string{"s/out", "s/big.bin"}}}}
	ps := Derive(st, t0)
	want := "link-blocked:s/dir link-dangling:s/gone link-pending:s/old link-skipped:s/out skipped:s"
	if got := keys(ps); got != want {
		t.Fatalf("keys = %q, want %q", got, want)
	}
	if ps[0].Severity != Error {
		t.Fatal("a blocked link is an error")
	}
	for _, p := range ps {
		if p.Key == "skipped:s" && p.Message != "s: 1 file skipped (symlinks, or over 10 MB)" {
			t.Fatalf("the generic skipped count includes reported links: %q", p.Message)
		}
	}
}

func TestD23_LostLinkIsAWarningForAnHour(t *testing.T) {
	ev := engine.Event{Kind: engine.EvLinkLost, Path: "s/l", Detail: "../r/a", Time: t0}
	if ps := Derive(Input{Events: []engine.Event{ev}}, t0.Add(30*time.Minute)); len(ps) != 1 || ps[0].Severity != Warning {
		t.Fatalf("got %+v", ps)
	}
	if ps := Derive(Input{Events: []engine.Event{ev}}, t0.Add(2*time.Hour)); len(ps) != 0 {
		t.Fatalf("still there after an hour: %+v", ps)
	}
}
