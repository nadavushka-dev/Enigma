package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestLayoutShowsThreePanesAndFooter(t *testing.T) {
	m := started(t, newFake(t))
	viewHas(t, m, "Machines", "Files", "Activity", "tab", "quit")
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 30 {
		t.Fatalf("%d lines, want the terminal's 30", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 100 {
			t.Fatalf("line %d is %d wide", i, w)
		}
	}
}

func TestTabMovesFocus(t *testing.T) {
	m := started(t, newFake(t))
	for _, want := range []int{1, 2, 0} {
		drive(t, m, key("tab"))
		if m.focus != want {
			t.Fatalf("focus %d, want %d", m.focus, want)
		}
	}
	drive(t, m, key("shift+tab"))
	if m.focus != 2 {
		t.Fatalf("shift+tab: focus %d", m.focus)
	}
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		m := started(t, newFake(t))
		_, cmd := m.Update(key(k))
		if cmd == nil {
			t.Fatalf("%s: no command", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%s does not quit", k)
		}
	}
}

func TestDaemonDownShowsABanner(t *testing.T) {
	f := newFake(t)
	f.down = errDown
	m := started(t, f)
	viewHas(t, m, "daemon is not running")
	f.down = nil
	drive(t, m, m.fetchStatus()())
	if strings.Contains(m.View(), "daemon is not running") {
		t.Fatal("banner stays after the daemon is back")
	}
}

func TestHelpOverlayOpensAndCloses(t *testing.T) {
	m := started(t, newFake(t))
	drive(t, m, key("?"))
	viewHas(t, m, "move focus between panes")
	drive(t, m, key("j"))
	if strings.Contains(m.View(), "move focus between panes") {
		t.Fatal("help stays open")
	}
}

func TestConfirmDialog(t *testing.T) {
	m := started(t, newFake(t))
	yes, alt := 0, 0
	open := OpenConfirm{Question: "Remove devbox?", Yes: func() tea.Msg { yes++; return nil },
		AltKey: "u", AltLabel: "remove and uninstall", Alt: func() tea.Msg { alt++; return nil }}
	drive(t, m, open)
	viewHas(t, m, "Remove devbox?", "remove and uninstall")
	drive(t, m, key("tab")) // keys go to the dialog, not the panes
	if m.focus != 0 || m.modal == nil {
		t.Fatal("the dialog must take every key")
	}
	drive(t, m, key("y"))
	if yes != 1 || m.modal != nil {
		t.Fatalf("y: yes=%d modal=%v", yes, m.modal)
	}
	drive(t, m, open, key("u"))
	if alt != 1 {
		t.Fatal("alt key")
	}
	drive(t, m, open, key("n"))
	if m.modal != nil || yes != 1 {
		t.Fatal("n must just close")
	}
}

func TestProgressDialogStreamsOutput(t *testing.T) {
	f := newFake(t)
	m := started(t, f)
	drive(t, m, OpenProgress{Title: "Adding devbox", Run: func(ctx context.Context, out io.Writer) error {
		fmt.Fprintln(out, "1. detect the platform")
		fmt.Fprint(out, "   linux/arm64")
		return nil
	}})
	viewHas(t, m, "Adding devbox", "1. detect the platform", "linux/arm64", "done")
	drive(t, m, key("esc"))
	if m.modal != nil {
		t.Fatal("esc closes a finished progress dialog")
	}
}

func TestProgressDialogShowsFailure(t *testing.T) {
	m := started(t, newFake(t))
	drive(t, m, OpenProgress{Title: "Adding nas", Run: func(ctx context.Context, out io.Writer) error {
		return fmt.Errorf("ssh nas: Connection refused")
	}})
	viewHas(t, m, "failed: ssh nas: Connection refused")
}

func TestViewerShowsBothSidesAndMarks(t *testing.T) {
	m := started(t, newFake(t))
	drive(t, m, OpenViewer{Title: "notes/todo.md",
		Left:  Column{Title: "original", Lines: []string{"same", "mine"}, Marks: []bool{false, true}},
		Right: Column{Title: "conflict copy", Lines: []string{"same", "theirs"}, Marks: []bool{false, true}}})
	viewHas(t, m, "original", "conflict copy", "mine", "theirs", "▌")
}

func TestShowErrorUsesTheFooterUntilTheNextKey(t *testing.T) {
	m := started(t, newFake(t))
	drive(t, m, ShowError{Err: fmt.Errorf("~/x is already synced")})
	viewHas(t, m, "already synced")
	drive(t, m, key("j"))
	if strings.Contains(m.View(), "already synced") {
		t.Fatal("error stays")
	}
}

// Keys typed faster than they are read arrive as one message with several
// runes ("ljjj"); each must act as its own key.
func TestBatchedRunesAreSeparateKeys(t *testing.T) {
	f := newFake(t)
	f.write(t, "a/x", "1")
	f.write(t, "b/x", "1")
	f.write(t, "c/x", "1")
	m := started(t, f)
	drive(t, m, key("tab"), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("jj")})
	if p := m.panes[1].(*filesPane); p.cursor != 2 {
		t.Fatalf("cursor %d after \"jj\", want 2", p.cursor)
	}
	// A paste stays one message (it is text, not keys).
	drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("kk"), Paste: true})
	if p := m.panes[1].(*filesPane); p.cursor != 2 {
		t.Fatalf("a paste moved the cursor to %d", p.cursor)
	}
}
