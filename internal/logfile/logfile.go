// Package logfile is the daemon's size-rotated log (SPEC §10): when a write
// would grow the file past max, the file becomes <path>.1 (replacing any
// older backup) and a new one is started.
package logfile

import (
	"os"
	"path/filepath"
	"sync"
)

type Writer struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func Open(path string, max int64) (*Writer, error) {
	w := &Writer{path: path, max: max}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return w, w.open()
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, fi.Size()
	return nil
}

// Write writes p whole: a line is never split across two files.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size > 0 && w.size+int64(len(p)) > w.max {
		w.f.Close()
		os.Rename(w.path, w.path+".1")
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}
