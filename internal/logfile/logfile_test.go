package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRotatesAtMaxKeepingOneBackup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "logs", "enigma.log")
	w, err := Open(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Write([]byte("aaaaaaaa\n")) // 9 bytes
	w.Write([]byte("bbbbbbbb\n")) // would exceed 10: rotate first
	w.Write([]byte("cccccccc\n")) // rotate again; the "a" backup is dropped
	cur, _ := os.ReadFile(p)
	old, _ := os.ReadFile(p + ".1")
	if string(cur) != "cccccccc\n" || string(old) != "bbbbbbbb\n" {
		t.Fatalf("cur=%q old=%q", cur, old)
	}
}

func TestAppendsToExistingLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "enigma.log")
	os.WriteFile(p, []byte("before\n"), 0o600)
	w, _ := Open(p, 1<<20)
	w.Write([]byte("after\n"))
	w.Close()
	if b, _ := os.ReadFile(p); string(b) != "before\nafter\n" {
		t.Fatalf("got %q", b)
	}
}

func TestConcurrentWritesKeepLinesWhole(t *testing.T) {
	p := filepath.Join(t.TempDir(), "enigma.log")
	w, _ := Open(p, 200)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				w.Write([]byte("0123456789\n"))
			}
		}()
	}
	wg.Wait()
	w.Close()
	for _, f := range []string{p, p + ".1"} {
		b, _ := os.ReadFile(f)
		for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
			if line != "0123456789" {
				t.Fatalf("%s: torn line %q", f, line)
			}
		}
	}
}
