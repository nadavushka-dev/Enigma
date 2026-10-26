package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/fuzzy"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

// fake is a Backend for tests: a real temp home, canned daemon state, and
// a record of every call.
type fake struct {
	mu       sync.Mutex
	home     string
	status   daemon.StatusReport
	machines []proto.MachineRecord
	hosts    []string
	events   []engine.Event
	down     error // Status fails with this
	calls    []string
	steps    []string // what AddMachine/RemoveMachine print
	opErr    error
	index    *fuzzy.Index
	groups   []engine.GroupInfo
}

func newFake(t *testing.T) *fake {
	home := t.TempDir()
	return &fake{home: home, status: daemon.StatusReport{Machine: "mac", MachineID: "id-mac", Version: "v1.0.0"},
		machines: []proto.MachineRecord{{ID: "id-mac", Name: "mac"}}}
}

func (f *fake) call(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }
func (f *fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}
func (f *fake) Home() string { return f.home }

func (f *fake) Status() (daemon.StatusReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down != nil {
		return daemon.StatusReport{}, f.down
	}
	return f.status, nil
}

func (f *fake) Machines() ([]proto.MachineRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.machines, nil
}

func (f *fake) Hosts() ([]string, error) { return f.hosts, nil }
func (f *fake) Add(abs string, group string) error {
	if group == "" {
		f.call("add " + abs)
	} else {
		f.call("add " + abs + " " + group)
	}
	return f.opErr
}

func (f *fake) MoveEntry(abs string, group string) error {
	f.call("move " + abs + " " + group)
	return f.opErr
}

func (f *fake) Groups() ([]engine.GroupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.groups == nil {
		return []engine.GroupInfo{{GroupRecord: proto.GroupRecord{ID: "main", Name: "main", All: true, Members: []string{"id-mac"}}, MemberNames: []string{"mac"}}}, nil
	}
	return f.groups, nil
}

func (f *fake) GroupOp(req daemon.GroupRequest) (string, error) {
	switch req.Action {
	case "create":
		f.call(fmt.Sprintf("group create %s add=%v", req.Name, req.Add))
	case "delete":
		f.call("group delete " + req.ID)
	case "rename":
		f.call("group rename " + req.ID + " " + req.Name)
	case "members":
		f.call(fmt.Sprintf("group members %s add=%v remove=%v", req.ID, req.Add, req.Remove))
	}
	return "new-id", f.opErr
}
func (f *fake) Remove(abs string) error { f.call("remove " + abs); return f.opErr }

func (f *fake) SetLimits(l engine.Limits) (engine.Limits, error) {
	f.call(fmt.Sprintf("set-limits %d %d", l.MaxFileSize, l.MaxFiles))
	if f.opErr != nil {
		return engine.Limits{}, f.opErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if l.MaxFileSize > 0 {
		f.status.Limits.MaxFileSize = l.MaxFileSize
	}
	if l.MaxFiles > 0 {
		f.status.Limits.MaxFiles = l.MaxFiles
	}
	return f.status.Limits, nil
}

func (f *fake) AddMachine(ctx context.Context, alias string, groups []string, out io.Writer) error {
	if groups == nil {
		f.call("add-machine " + alias)
	} else {
		f.call(fmt.Sprintf("add-machine %s groups=%v", alias, groups))
	}
	for _, s := range f.steps {
		fmt.Fprintln(out, s)
	}
	return f.opErr
}

func (f *fake) RemoveMachine(ctx context.Context, name string, uninstall bool, out io.Writer) error {
	f.call(fmt.Sprintf("remove-machine %s uninstall=%v", name, uninstall))
	for _, s := range f.steps {
		fmt.Fprintln(out, s)
	}
	return f.opErr
}

func (f *fake) Events(after uint64, wait time.Duration) ([]engine.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []engine.Event
	for _, ev := range f.events {
		if ev.Seq > after {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (f *fake) HomeIndex() *fuzzy.Index {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.index == nil {
		f.index = fuzzy.Build(f.home, fuzzy.Options{})
	}
	return f.index
}

func (f *fake) ReadFile(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(f.home, filepath.FromSlash(rel)))
}

func (f *fake) write(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(f.home, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- driving a Model ---------------------------------------------------------------

// drive runs each msg through m with every command it returns, recursively
// (ticks and long polls excluded), before the next msg, so a test sees the
// settled result: drive(t, m, key("x"), key("y")) answers the dialog x opens.
func drive(t *testing.T, m *Model, msgs ...tea.Msg) {
	t.Helper()
	for _, first := range msgs {
		queue := []tea.Msg{first}
		for steps := 0; len(queue) > 0; steps++ {
			if steps > 500 {
				t.Fatal("drive: too many steps")
			}
			msg := queue[0]
			queue = queue[1:]
			_, cmd := m.Update(msg)
			queue = append(queue, run(cmd)...)
		}
	}
}

// run executes cmd and the commands it batches, skipping slow ones.
func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(300 * time.Millisecond): // a tick or a long poll
		return nil
	}
	switch msg := msg.(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, run(c)...)
		}
		return out
	case nil, tickMsg, pollMsg:
		return nil
	case EventsMsg:
		if len(msg.Events) == 0 && msg.Err == nil {
			return nil // an empty poll would only poll again
		}
	}
	return []tea.Msg{msg}
}

func key(k string) tea.KeyMsg {
	switch k {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	case "ctrl+n":
		return tea.KeyMsg{Type: tea.KeyCtrlN}
	case "ctrl+p":
		return tea.KeyMsg{Type: tea.KeyCtrlP}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// started is a Model over f, sized and with its first status loaded.
func started(t *testing.T, f *fake) *Model {
	t.Helper()
	m := New(f)
	drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	drive(t, m, m.fetchStatus()())
	return m
}

func viewHas(t *testing.T, m *Model, wants ...string) {
	t.Helper()
	v := m.View()
	for _, w := range wants {
		if !strings.Contains(v, w) {
			t.Fatalf("view missing %q:\n%s", w, v)
		}
	}
}

var errDown = errors.New("the enigma daemon is not running (start it with `enigma service install`)")

// machineRec is a member record for tests.
func machineRec(name string) proto.MachineRecord {
	return proto.MachineRecord{ID: "id-" + name, Name: name, Alias: name, Target: "ssh://" + name}
}
