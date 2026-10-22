package engine

import (
	"os"
	"strings"
	"testing"
	"time"
)

func (m *machine) entry(rel string) EntryStatus {
	m.t.Helper()
	for _, es := range m.e.Status().Entries {
		if es.Path == rel {
			return es
		}
	}
	m.t.Fatalf("%s: no entry %s", m.name, rel)
	return EntryStatus{}
}

func hasEvent(m *machine, kind, path, textPart string) func() bool {
	return func() bool {
		for _, ev := range m.e.Events(0) {
			if ev.Kind == kind && ev.Path == path && strings.Contains(ev.Detail, textPart) {
				return true
			}
		}
		return false
	}
}

// SPEC §8: a folder that grows past the limit keeps syncing what it has;
// new files past the limit are not synced, and sync once there is room.
func TestFolderGrowingPastTheLimitSkipsOnlyNewFiles(t *testing.T) {
	var n notes
	three := func(o *Options) { o.MaxFiles = 3 }
	a, b := newMachine(t, "a", three, noWatch, n.opt), newMachine(t, "b", three)
	a.write("d/1", "one")
	a.write("d/2", "two")
	a.add("d")
	link(t, a, b)
	eventually(t, "d/2 synced", hasFile(b, "d/2", "two"))

	a.write("d/3", "three")
	a.write("d/4", "four")
	a.write("d/5", "five")
	a.e.Rescan()
	eventually(t, "d/3 synced (room for one)", hasFile(b, "d/3", "three"))
	time.Sleep(100 * time.Millisecond)
	for _, rel := range []string{"d/4", "d/5"} {
		if b.read(rel) != "<missing>" {
			t.Fatalf("%s synced past the limit", rel)
		}
	}
	if got := strings.Join(a.entry("d").OverLimit, ","); got != "d/4,d/5" {
		t.Fatalf("OverLimit = %q, want d/4,d/5", got)
	}
	eventually(t, "error event", hasEvent(a, "error", "d", "over the 3-file limit"))
	eventually(t, "notification", n.has("d", "2 new files not synced"))

	a.write("d/1", "one, edited")
	a.e.Rescan()
	eventually(t, "a tracked file still syncs", hasFile(b, "d/1", "one, edited"))

	os.Remove(a.path("d/2"))
	a.e.Rescan()
	eventually(t, "d/4 synced once there is room", hasFile(b, "d/4", "four"))
	if got := strings.Join(a.entry("d").OverLimit, ","); got != "d/5" {
		t.Fatalf("OverLimit = %q, want d/5", got)
	}
}

// A file removed while the folder is over the limit makes room at once,
// without waiting for the periodic rescan.
func TestRoomInAFullFolderIsUsedAtOnce(t *testing.T) {
	two := func(o *Options) { o.MaxFiles = 2 }
	a, b := newMachine(t, "a", two), newMachine(t, "b", two)
	a.write("d/1", "one")
	a.write("d/2", "two")
	a.add("d")
	link(t, a, b)
	eventually(t, "d/2 synced", hasFile(b, "d/2", "two"))
	a.write("d/3", "three")
	eventually(t, "d/3 over the limit", func() bool { return contains(a.entry("d").OverLimit, "d/3") })
	os.Remove(a.path("d/1"))
	eventually(t, "d/3 synced", hasFile(b, "d/3", "three"))
	if ol := a.entry("d").OverLimit; len(ol) != 0 {
		t.Fatalf("OverLimit = %v, want none", ol)
	}
}

func TestLimitsAreAGroupSetting(t *testing.T) {
	a, b, c := newMachine(t, "a"), newMachine(t, "b"), newMachine(t, "c")
	if got := a.e.Limits(); got != (Limits{MaxFileSize: DefaultMaxFileSize, MaxFiles: DefaultMaxFiles}) {
		t.Fatalf("default limits %+v", got)
	}
	if err := a.e.SetLimits(Limits{MaxFiles: 5000}); err != nil { // a zero field keeps its value
		t.Fatal(err)
	}
	link(t, a, b) // catch-up carries it
	link(t, b, c) // and it hops
	want := Limits{MaxFileSize: DefaultMaxFileSize, MaxFiles: 5000}
	eventually(t, "c has the limits", func() bool { return c.e.Limits() == want })
	if err := c.e.SetLimits(Limits{MaxFileSize: 25 << 20}); err != nil {
		t.Fatal(err)
	}
	want.MaxFileSize = 25 << 20
	eventually(t, "the latest change wins on a", func() bool { return a.e.Limits() == want })
	if got := a.e.Status().Limits; got != want {
		t.Fatalf("Status().Limits = %+v", got)
	}
	eventually(t, "a limits event", hasEvent(a, "limits", "", "25 MB"))
	a.restart()
	if got := a.e.Limits(); got != want {
		t.Fatalf("after restart %+v", got)
	}
}

func TestRaisingTheSizeLimitSyncsSkippedFiles(t *testing.T) {
	small := func(o *Options) { o.MaxFileSize = 8 }
	a, b := newMachine(t, "a", small), newMachine(t, "b", small)
	a.write("d/ok", "fine")
	a.write("d/big", "way too large")
	a.add("d")
	link(t, a, b)
	eventually(t, "ok synced", hasFile(b, "d/ok", "fine"))
	if err := b.e.SetLimits(Limits{MaxFileSize: 100}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "big synced after the raise", hasFile(b, "d/big", "way too large"))
	if sk := a.entry("d").Skipped; len(sk) != 0 {
		t.Fatalf("still skipped: %v", sk)
	}
}

func TestRaisingTheFileLimitSyncsWaitingFiles(t *testing.T) {
	two := func(o *Options) { o.MaxFiles = 2 }
	a, b := newMachine(t, "a", two), newMachine(t, "b", two)
	a.write("d/1", "one")
	a.add("d")
	link(t, a, b)
	a.write("d/2", "two")
	a.write("d/3", "three")
	eventually(t, "d/3 over the limit", func() bool { return len(a.entry("d").OverLimit) == 1 })
	if err := a.e.SetLimits(Limits{MaxFiles: 10}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "d/2 synced", hasFile(b, "d/2", "two"))
	eventually(t, "d/3 synced", hasFile(b, "d/3", "three"))
}

func TestLimitsOutOfRangeAreRefused(t *testing.T) {
	a := newMachine(t, "a")
	for _, tc := range []struct {
		l    Limits
		want string
	}{
		{Limits{MaxFileSize: 65 << 20}, "at most 64 MB"},
		{Limits{MaxFiles: 20001}, "at most 20,000 files"},
		{Limits{MaxFileSize: -1}, "file size must be positive"},
		{Limits{MaxFiles: -1}, "file count must be positive"},
	} {
		err := a.e.SetLimits(tc.l)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("SetLimits(%+v) = %v, want %q", tc.l, err, tc.want)
		}
	}
	if got := a.e.Limits(); got != (Limits{MaxFileSize: DefaultMaxFileSize, MaxFiles: DefaultMaxFiles}) {
		t.Fatalf("a refused change applied: %+v", got)
	}
}

// The adding error names the limit and how to raise it.
func TestFolderOverTheLimitErrorSaysHowToRaiseIt(t *testing.T) {
	a := newMachine(t, "a", func(o *Options) { o.MaxFiles = 2 })
	for _, n := range []string{"1", "2", "3"} {
		a.write("d/"+n, n)
	}
	err := a.e.Add(a.path("d"), nil)
	if err == nil || !strings.Contains(err.Error(), "enigma limits --files") {
		t.Fatalf("Add = %v, want a hint at enigma limits", err)
	}
}

// A machine never writes a received file over its own limit (a limit
// change still on its way, or a peer that does not check).
func TestReceiverRefusesAFileOverItsLimit(t *testing.T) {
	var n notes
	a := newMachine(t, "a", func(o *Options) { o.MaxFileSize = 100 })
	b := newMachine(t, "b", func(o *Options) { o.MaxFileSize = 8 }, n.opt)
	a.write("d/ok", "fine")
	a.add("d")
	link(t, a, b)
	eventually(t, "ok synced", hasFile(b, "d/ok", "fine"))
	a.write("d/big", "way too large")
	eventually(t, "b reports the refusal", hasEvent(b, "error", "d/big", "over the 8 B file limit"))
	eventually(t, "b notifies", n.has("d/big", "not received"))
	if b.read("d/big") != "<missing>" {
		t.Fatal("b wrote a file over its limit")
	}
}
