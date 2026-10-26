package main

import (
	"os"
	"testing"
)

// Regression: /dev/null is a character device, so a subprocess with no
// stdin looked interactive and a question was "answered" by EOF.
func TestDevNullIsNotATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("/dev/null counted as a terminal")
	}
	tmp, _ := os.CreateTemp(t.TempDir(), "x")
	defer tmp.Close()
	if isTerminal(tmp) {
		t.Fatal("a regular file counted as a terminal")
	}
}
