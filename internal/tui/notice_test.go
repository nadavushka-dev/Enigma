package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/problems"
)

func withProblems(t *testing.T) (*fake, *Model) {
	f := newFake(t)
	f.status.Peers = []daemon.PeerReport{{Name: "laptop", Target: "laptop", LastError: "ssh: connection refused"}}
	f.machines = append(f.machines, machineRec("laptop"))
	f.status.Problems = []problems.Problem{
		{Key: "link:laptop", Severity: problems.Error, Since: time.Now().Add(-3 * time.Minute), Peer: "laptop",
			Message: "laptop link down (ssh: connection refused)", Hint: "enigma doctor checks the links"},
		{Key: "skipped:bin", Severity: problems.Warning, Since: time.Now(), Path: "bin",
			Message: "bin: 2 files skipped (symlinks, or over 10 MB)", Hint: "enigma limits --file-size raises the size limit"},
	}
	if err := os.MkdirAll(filepath.Join(f.home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	return f, started(t, f)
}

func noticeLine(m *Model) string {
	lines := strings.Split(m.View(), "\n")
	return lines[len(lines)-2] // above the key hints
}

func TestD20_NoticeLineSaysAllGood(t *testing.T) {
	m := started(t, newFake(t))
	if l := noticeLine(m); !strings.Contains(l, "all good") {
		t.Fatalf("notice: %q", l)
	}
}

func TestD20_NoticeLineShowsTheProblemCountAndTheWorst(t *testing.T) {
	_, m := withProblems(t)
	l := noticeLine(m)
	for _, want := range []string{"! 2 problems: laptop link down (ssh: connection refused)", "p"} {
		if !strings.Contains(l, want) {
			t.Fatalf("notice %q missing %q", l, want)
		}
	}
	if !strings.Contains(m.View(), "p problems") {
		t.Fatal("key hints must mention p")
	}
}

func TestD20_ProblemsModalListsEveryProblemWithItsHint(t *testing.T) {
	_, m := withProblems(t)
	drive(t, m, key("p"))
	if m.modal == nil {
		t.Fatal("p opens the problems modal")
	}
	viewHas(t, m, "laptop link down", "enigma doctor checks the links", "bin: 2 files skipped", "enigma limits --file-size", "3m")
	drive(t, m, key("esc"))
	if m.modal != nil {
		t.Fatal("esc closes it")
	}
}

func TestD20_ProblemsModalWithNothingToShow(t *testing.T) {
	m := started(t, newFake(t))
	drive(t, m, key("p"))
	viewHas(t, m, "no problems")
	drive(t, m, key("q"))
	if m.modal != nil {
		t.Fatal("q closes it")
	}
}

func TestD20_EnterOnAProblemJumpsToItsRow(t *testing.T) {
	_, m := withProblems(t)
	drive(t, m, key("p"), key("enter")) // first problem: the laptop link
	if m.modal != nil || m.focus != 0 {
		t.Fatalf("modal=%v focus=%d, want the Machines pane", m.modal, m.focus)
	}
	mp := m.panes[0].(*machinesPane)
	if mp.rows[mp.cur].name != "laptop" {
		t.Fatalf("cursor on %q", mp.rows[mp.cur].name)
	}
	drive(t, m, key("p"), key("j"), key("enter")) // second: the bin entry
	if m.focus != 1 {
		t.Fatalf("focus %d, want the Files pane", m.focus)
	}
	fp := m.panes[1].(*filesPane)
	if n := fp.current(); n == nil || n.rel != "bin" {
		t.Fatalf("cursor on %v", n)
	}
}

func TestD20_ActionResultShowsForAWhileThenClears(t *testing.T) {
	m := started(t, newFake(t))
	drive(t, m, ShowNotice{Text: "added ~/.claude to pair"})
	if l := noticeLine(m); !strings.Contains(l, "added ~/.claude to pair") {
		t.Fatalf("notice: %q", l)
	}
	drive(t, m, key("j")) // a key does not clear a result
	if l := noticeLine(m); !strings.Contains(l, "added ~/.claude to pair") {
		t.Fatalf("after a key: %q", l)
	}
	drive(t, m, noticeExpiredMsg{seq: m.noticeSeq})
	if l := noticeLine(m); !strings.Contains(l, "all good") {
		t.Fatalf("after expiry: %q", l)
	}
}

func TestD20_FailedActionStaysInRedUntilTheNextKey(t *testing.T) {
	_, m := withProblems(t)
	drive(t, m, ShowError{Err: fmt.Errorf("refused: ~/.zshrc is already synced in main")})
	if l := noticeLine(m); !strings.Contains(l, "refused: ~/.zshrc is already synced in main") {
		t.Fatalf("notice: %q", l)
	}
	drive(t, m, key("j"))
	if l := noticeLine(m); !strings.Contains(l, "2 problems") {
		t.Fatalf("after a key the problems are back: %q", l)
	}
}
