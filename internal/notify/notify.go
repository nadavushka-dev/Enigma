// Package notify raises desktop notifications (SPEC §12.3): one per path
// per window, never waiting on the desktop command.
package notify

import (
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Window is the default rate limit: one notification per path per 10 minutes.
const Window = 10 * time.Minute

// Sender delivers one notification. It may be slow; Notifier never waits on it.
type Sender func(title, body string) error

// Notifier rate-limits notifications per path and sends them off the
// caller's goroutine. A nil Sender makes it silent, which is what tests use.
type Notifier struct {
	send   Sender
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
}

func New(send Sender, window time.Duration) *Notifier {
	if window <= 0 {
		window = Window
	}
	return &Notifier{send: send, window: window, now: time.Now, last: map[string]time.Time{}}
}

// Notify raises a notification about path unless one was raised for it
// within the window. It returns at once.
func (n *Notifier) Notify(path, title, body string) {
	if n == nil || n.send == nil {
		return
	}
	n.mu.Lock()
	t := n.now()
	if last, ok := n.last[path]; ok && t.Sub(last) < n.window {
		n.mu.Unlock()
		return
	}
	n.last[path] = t
	for p, at := range n.last { // keep the map from growing forever
		if t.Sub(at) >= n.window {
			delete(n.last, p)
		}
	}
	n.mu.Unlock()
	go n.send(title, body)
}

// Desktop returns this platform's sender: osascript on macOS, notify-send
// on Linux when it is installed, else nil.
func Desktop() Sender {
	switch runtime.GOOS {
	case "darwin":
		return func(title, body string) error {
			return exec.Command("osascript", "-e", appleScript(title, body)).Run()
		}
	case "linux":
		bin, err := exec.LookPath("notify-send")
		if err != nil {
			return nil
		}
		return func(title, body string) error {
			return exec.Command(bin, "--app-name=Enigma", title, body).Run()
		}
	}
	return nil
}

// appleScript builds the display notification script. The text goes in as
// AppleScript string literals (backslash, quote and newline escaped); the
// script is passed to osascript as an argument, never through a shell.
func appleScript(title, body string) string {
	return `display notification "` + appleQuote(body) + `" with title "` + appleQuote(title) + `"`
}

var appleQuoter = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)

func appleQuote(s string) string { return appleQuoter.Replace(s) }
