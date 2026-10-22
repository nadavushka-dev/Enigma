package notify

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu   sync.Mutex
	got  []string
	hold chan struct{} // if set, Send blocks on it
}

func (r *recorder) send(title, body string) error {
	if r.hold != nil {
		<-r.hold
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, title+": "+body)
	return nil
}

func (r *recorder) has(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, g := range r.got {
		if g == s {
			return true
		}
	}
	return false
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOnePerPathPerWindow(t *testing.T) {
	r := &recorder{}
	now := time.Unix(1000, 0)
	n := New(r.send, 10*time.Minute)
	n.now = func() time.Time { return now }

	n.Notify("a.md", "Enigma", "first")
	n.Notify("a.md", "Enigma", "again at once")
	n.Notify("b.md", "Enigma", "other path")
	now = now.Add(9 * time.Minute)
	n.Notify("a.md", "Enigma", "still inside the window")
	waitFor(t, "two sends", func() bool { return r.count() == 2 })
	time.Sleep(20 * time.Millisecond)
	if r.count() != 2 {
		t.Fatalf("sends = %v", r.got)
	}
	now = now.Add(2 * time.Minute)
	n.Notify("a.md", "Enigma", "after the window")
	waitFor(t, "third send", func() bool { return r.count() == 3 })
	for _, want := range []string{"Enigma: first", "Enigma: other path", "Enigma: after the window"} {
		if !r.has(want) { // sends run concurrently, so order is not promised
			t.Fatalf("missing %q in %v", want, r.got)
		}
	}
}

func TestNotifyDoesNotWaitForSend(t *testing.T) {
	r := &recorder{hold: make(chan struct{})}
	n := New(r.send, time.Minute)
	done := make(chan struct{})
	go func() { n.Notify("p", "t", "b"); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify blocked on a slow sender")
	}
	close(r.hold)
	waitFor(t, "send", func() bool { return r.count() == 1 })
}

func TestNilSenderIsSilent(t *testing.T) {
	n := New(nil, time.Minute)
	n.Notify("p", "t", "b") // must not panic
}

func TestAppleScriptQuotesText(t *testing.T) {
	got := appleScript(`say "hi"`, `C:\x`+"\n"+`done`)
	want := `display notification "C:\\x\ndone" with title "say \"hi\""`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestDesktopNeedsACommand(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	t.Setenv("PATH", t.TempDir())
	if Desktop() != nil {
		t.Fatal("no notify-send on PATH, yet a sender")
	}
}
