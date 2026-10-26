package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/enigma/internal/engine"
)

func typeText(t *testing.T, m *Model, s string) {
	t.Helper()
	for _, r := range s {
		drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func TestPromptTypesEditsAndSubmits(t *testing.T) {
	m := started(t, newFake(t))
	var got string
	drive(t, m, OpenPrompt{Title: "New group", Label: "name", Initial: "pai",
		Submit: func(s string) tea.Cmd { got = s; return nil }})
	viewHas(t, m, "New group", "name", "pai")
	typeText(t, m, "r")
	drive(t, m, key("q")) // q is text here, not quit
	if m.modal == nil {
		t.Fatal("q must not close a prompt")
	}
	drive(t, m, key("backspace"), key("enter"))
	if got != "pair" || m.modal != nil {
		t.Fatalf("got %q modal=%v", got, m.modal)
	}
}

func TestPromptEscCancels(t *testing.T) {
	m := started(t, newFake(t))
	called := false
	drive(t, m, OpenPrompt{Title: "x", Label: "y", Submit: func(string) tea.Cmd { called = true; return nil }})
	typeText(t, m, "abc")
	drive(t, m, key("esc"))
	if called || m.modal != nil {
		t.Fatal("esc must close without submitting")
	}
}

func TestPickTogglesWithSpaceAndSubmitsTheChecked(t *testing.T) {
	m := started(t, newFake(t))
	var got []string
	drive(t, m, OpenPick{Title: "Members of pair", Items: []PickItem{{Label: "mac", Checked: true}, {Label: "devbox"}, {Label: "laptop"}},
		Submit: func(ls []string) tea.Cmd { got = ls; return nil }})
	viewHas(t, m, "Members of pair", "[x] mac", "[ ] devbox", "[ ] laptop")
	drive(t, m, key("j"), key("space"), key("j"), key("space"), key("space"), key("enter"))
	if strings.Join(got, ",") != "mac,devbox" || m.modal != nil {
		t.Fatalf("got %v", got)
	}
}

func TestLimitsKeyPromptsForBothAndSetsThem(t *testing.T) {
	f := newFake(t)
	f.status.Limits = engine.Limits{MaxFileSize: 10 << 20, MaxFiles: 2000}
	m := started(t, f)
	drive(t, m, key("L"))
	viewHas(t, m, "per file", "10 MB")
	drive(t, m, key("ctrl+u"))
	typeText(t, m, "25MB")
	drive(t, m, key("enter"))
	viewHas(t, m, "files per folder", "2000")
	drive(t, m, key("ctrl+u"))
	typeText(t, m, "5000")
	drive(t, m, key("enter"))
	calls := f.Calls()
	if len(calls) != 1 || calls[0] != "set-limits 26214400 5000" {
		t.Fatalf("calls %v", calls)
	}
	if l := noticeLine(m); !strings.Contains(l, "limits set for every machine: 25 MB per file, 5,000 files per folder") {
		t.Fatalf("notice %q", l)
	}
}

func TestLimitsBadSizeIsAnErrorAndNothingIsSet(t *testing.T) {
	f := newFake(t)
	m := started(t, f)
	drive(t, m, key("L"), key("ctrl+u"))
	typeText(t, m, "lots")
	drive(t, m, key("enter"))
	if len(f.Calls()) != 0 || m.modal != nil {
		t.Fatalf("calls %v modal %v", f.Calls(), m.modal)
	}
	if l := noticeLine(m); !strings.Contains(l, `wants a size like 25MB, not "lots"`) {
		t.Fatalf("notice %q", l)
	}
}

func TestLimitsDaemonErrorReachesTheNoticeLine(t *testing.T) {
	f := newFake(t)
	f.opErr = errors.New("64 MB is the ceiling")
	f.status.Limits = engine.Limits{MaxFileSize: 10 << 20, MaxFiles: 2000}
	m := started(t, f)
	drive(t, m, key("L"), key("enter"), key("ctrl+u"))
	typeText(t, m, "3")
	drive(t, m, key("enter"))
	if l := noticeLine(m); !strings.Contains(l, "64 MB is the ceiling") {
		t.Fatalf("notice %q", l)
	}
}
