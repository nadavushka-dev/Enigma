package engine

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/notify"
)

type notes struct {
	mu  sync.Mutex
	got []string // "path|body"
}

func (n *notes) opt(o *Options) {
	o.Notify = func(path, title, body string) {
		n.mu.Lock()
		defer n.mu.Unlock()
		if title != "Enigma" {
			panic("title " + title)
		}
		n.got = append(n.got, path+"|"+body)
	}
}

func (n *notes) has(pathPrefix, bodyPart string) func() bool {
	return func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		for _, g := range n.got {
			p, b, _ := strings.Cut(g, "|")
			if strings.HasPrefix(p, pathPrefix) && strings.Contains(b, bodyPart) {
				return true
			}
		}
		return false
	}
}

func TestNotifiesOnConflictCopy(t *testing.T) {
	var cn, dn notes
	c, d := newMachine(t, "c", cn.opt), newMachine(t, "d", dn.opt)
	concurrentEdits(t, c, d, "x/g.md", "base\n", "on c\n", "on d\n")
	link(t, c, d)
	eventually(t, "conflict notification on c", cn.has("x/g.md", "~/x/g.md"))
	eventually(t, "conflict notification on d", dn.has("x/g.md", "~/x/g.md"))
	eventually(t, "body names the copy in tilde form", cn.has("x/g.md", "~/x/g.conflict-"))
}

func TestConflictNotificationsAreKeyedByTheOriginal(t *testing.T) {
	// Two conflict copies of one file inside the window are one thing for
	// the user: one notification (SPEC §12.3).
	var sent atomic.Int32
	n := notify.New(func(string, string) error { sent.Add(1); return nil }, 10*time.Minute)
	c, d := newMachine(t, "c", func(o *Options) { o.Notify = n.Notify }), newMachine(t, "d")
	concurrentEdits(t, c, d, "x/g.md", "base\n", "on c\n", "on d\n")
	cut := link(t, c, d)
	eventually(t, "first conflict", func() bool { return len(conflictCopies(c, "x", "g")) == 1 })
	eventually(t, "first notification", func() bool { return sent.Load() == 1 })
	cut()
	c.writeAged("x/g.md", "on c again\n", time.Hour)
	c.e.Rescan()
	d.write("x/g.md", "on d again\n")
	d.e.Rescan()
	link(t, c, d)
	eventually(t, "second conflict", func() bool { return len(conflictCopies(c, "x", "g")) == 2 })
	time.Sleep(200 * time.Millisecond)
	if got := sent.Load(); got != 1 {
		t.Fatalf("%d notifications for two conflicts on one file", got)
	}
}

func TestNotifiesOnVanishedRoot(t *testing.T) {
	var bn notes
	a, b := newMachine(t, "a"), newMachine(t, "b", bn.opt)
	a.write("vault/1.md", "1")
	a.add("vault")
	link(t, a, b)
	eventually(t, "synced", hasFile(b, "vault/1.md", "1"))
	if err := os.Rename(b.path("vault"), filepath.Join(filepath.Dir(b.home), "unmounted")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	b.e.Rescan()
	eventually(t, "vanished-root notification", bn.has("vault", "~/vault vanished"))
}

func TestNotifiesOnFailedWrite(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	var bn notes
	a, b := newMachine(t, "a"), newMachine(t, "b", bn.opt)
	a.write("n/f.md", "v1")
	a.add("n")
	link(t, a, b)
	eventually(t, "synced", hasFile(b, "n/f.md", "v1"))
	if err := os.Chmod(b.path("n"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(b.path("n"), 0o755) })
	a.write("n/f.md", "v2")
	eventually(t, "error event on b", b.hasEvent(EvError, "n/f.md", "a"))
	eventually(t, "failed-write notification on b", bn.has("n/f.md", "could not write ~/n/f.md"))
}
