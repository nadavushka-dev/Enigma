package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// A confirmation can carry a list (the entries a delete stops syncing):
// one per line, scrolled with j/k when it does not fit (SPEC §13.2).
func TestConfirmListsItemsOnTheirOwnLinesAndScrolls(t *testing.T) {
	f := groupsFake(t)
	m := started(t, f)
	drive(t, m, tea.WindowSizeMsg{Width: 80, Height: 16})
	var items []string
	for i := range 30 {
		items = append(items, fmt.Sprintf("~/item-%02d", i))
	}
	drive(t, m, OpenConfirm{Question: "Delete?", Items: items, Yes: func() tea.Msg { return nil }})
	v := m.View()
	for _, want := range []string{"Delete?", "~/item-00", "~/item-01", "more", "y yes"} {
		if !strings.Contains(v, want) {
			t.Fatalf("view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "~/item-29") || !strings.Contains(v, "  ~/item-00") {
		t.Fatalf("thirty items cannot fit in 16 rows, and each sits on its own line:\n%s", v)
	}
	drive(t, m, key("G"))
	v = m.View()
	if !strings.Contains(v, "~/item-29") || strings.Contains(v, "~/item-00") {
		t.Fatalf("G should scroll to the end:\n%s", v)
	}
	drive(t, m, key("k"))
	if v := m.View(); !strings.Contains(v, "~/item-28") || strings.Contains(v, "~/item-29") {
		t.Fatalf("k should scroll up one:\n%s", v)
	}
	drive(t, m, key("n"))
	if m.modal != nil {
		t.Fatal("n should close the confirmation")
	}
}

func TestGroupsDeleteListsTheEntries(t *testing.T) {
	f := groupsFake(t)
	m := groupsModel(t, f)
	drive(t, m, key("j"), key("x"))
	v := m.View()
	if !strings.Contains(v, "~/secret/") || !strings.Contains(v, "stop syncing") {
		t.Fatalf("delete confirmation:\n%s", v)
	}
	// the list is its own lines, not part of the sentence
	if strings.Contains(v, ": ~/secret/") {
		t.Fatalf("entries must be listed, not inlined:\n%s", v)
	}
}
