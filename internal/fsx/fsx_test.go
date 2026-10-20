package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHashIsSHA256Hex(t *testing.T) {
	// sha256("abc")
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := Hash([]byte("abc")); got != want {
		t.Fatalf("Hash = %s", got)
	}
}

func TestStatKinds(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	writeFile(t, f, "hi")
	os.Symlink(f, filepath.Join(dir, "link"))

	cases := map[string]Kind{"f": Regular, "missing": Missing, "link": Other, ".": Dir}
	for name, want := range cases {
		info, err := Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Kind != want {
			t.Errorf("%s: kind %v, want %v", name, info.Kind, want)
		}
	}
	info, _ := Stat(f)
	if info.Hash != Hash([]byte("hi")) || info.Size != 2 || info.Exec {
		t.Fatalf("info = %+v", info)
	}
}

func TestStatMissingParentIsMissing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "file"), "x")
	info, err := Stat(filepath.Join(dir, "file", "child")) // ENOTDIR
	if err != nil || info.Kind != Missing {
		t.Fatalf("info=%+v err=%v", info, err)
	}
}

func TestWriteCreatesParentsAndSetsMeta(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "c.sh")
	mt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixNano()
	info, err := Write(p, []byte("#!/bin/sh"), WriteOpts{Mtime: mt, Exec: true, ExpectAbsent: true})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Stat(p)
	if got.Hash != info.Hash || got.Hash != Hash([]byte("#!/bin/sh")) {
		t.Fatalf("hash mismatch %+v %+v", got, info)
	}
	if !got.Exec || got.Mtime != mt {
		t.Fatalf("meta not applied: %+v", got)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatalf("leftovers: %v", entries)
	}
}

func TestWritePreconditions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	writeFile(t, p, "one")
	// Expecting absence while a file exists must fail and leave it intact.
	if _, err := Write(p, []byte("two"), WriteOpts{ExpectAbsent: true}); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	// Expecting a stale hash must fail.
	if _, err := Write(p, []byte("two"), WriteOpts{ExpectHash: Hash([]byte("zzz"))}); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "one" {
		t.Fatalf("file clobbered: %q", b)
	}
	if _, err := Write(p, []byte("two"), WriteOpts{ExpectHash: Hash([]byte("one"))}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Fatalf("got %q", b)
	}
}

func TestWriteNeverClobbersNonRegular(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	writeFile(t, target, "keep")
	link := filepath.Join(dir, "link")
	os.Symlink(target, link)
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	for _, p := range []string{link, sub} {
		if _, err := Write(p, []byte("x"), WriteOpts{ExpectAbsent: true}); !errors.Is(err, ErrNotRegular) {
			t.Errorf("%s: err = %v, want ErrNotRegular", p, err)
		}
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatal("symlink target modified")
	}
}

func TestMoveIntoPreservesAndCreatesDirs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	writeFile(t, src, "data")
	dst := filepath.Join(dir, "trash", "x", "src.txt")
	got, err := MoveTo(src, dst, Hash([]byte("data")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source still exists")
	}
	if b, _ := os.ReadFile(got); string(b) != "data" {
		t.Fatalf("moved content %q", b)
	}
	// A second move to the same destination must not overwrite the first.
	writeFile(t, src, "data2")
	got2, err := MoveTo(src, dst, Hash([]byte("data2")))
	if err != nil || got2 == got {
		t.Fatalf("got2=%s err=%v", got2, err)
	}
	if b, _ := os.ReadFile(got); string(b) != "data" {
		t.Fatal("first moved file overwritten")
	}
}

func TestMoveToChecksPrecondition(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f")
	writeFile(t, src, "now")
	if _, err := MoveTo(src, filepath.Join(dir, "t", "f"), Hash([]byte("before"))); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("source moved despite failed precondition")
	}
}

func TestRemoveEmptyDirsStopsAtRoot(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	os.MkdirAll(deep, 0o755)
	writeFile(t, filepath.Join(root, "a", "keep"), "x")
	RemoveEmptyDirs(deep, root)
	if _, err := os.Stat(filepath.Join(root, "a", "b")); !os.IsNotExist(err) {
		t.Fatal("a/b should be removed")
	}
	if _, err := os.Stat(filepath.Join(root, "a")); err != nil {
		t.Fatal("a is not empty and must stay")
	}
	RemoveEmptyDirs(root, root)
	if _, err := os.Stat(root); err != nil {
		t.Fatal("root must never be removed")
	}
}

func TestMatcherDefaultsAndPatterns(t *testing.T) {
	m := NewMatcher([]string{"*.log", "build/out"})
	cases := map[string]bool{
		"notes.md": false, "a.log": true, "x/y/a.log": true,
		".git/HEAD": true, "sub/.git/config": true, "node_modules/p/i.js": true,
		".DS_Store": true, "f.swp": true, "f~": true, ".#lock": true, "#auto#": true,
		TempPrefix + "123": true,
		"build/out/bin":    true, "build/keep": false, "src/build/out": false,
	}
	for p, want := range cases {
		if got := m.Ignored(p); got != want {
			t.Errorf("Ignored(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestScanSkipsIgnoredSymlinksAndOversized(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.md"), "a")
	writeFile(t, filepath.Join(root, "sub", "b.md"), "b")
	writeFile(t, filepath.Join(root, "x.log"), "ignored")
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ignored")
	writeFile(t, filepath.Join(root, "big.bin"), "0123456789AB") // 12 bytes
	os.Symlink(filepath.Join(root, "a.md"), filepath.Join(root, "link.md"))

	res, err := Scan(root, NewMatcher([]string{"*.log"}), 10)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for rel := range res.Files {
		files = append(files, rel)
	}
	sort.Strings(files)
	if got := filepath.Join(files...); got != filepath.Join("a.md", "sub/b.md") {
		t.Fatalf("files = %v", files)
	}
	sort.Strings(res.Skipped)
	if len(res.Skipped) != 2 || res.Skipped[0] != "big.bin" || res.Skipped[1] != "link.md" {
		t.Fatalf("skipped = %v", res.Skipped)
	}
}

func TestScanMissingRootIsEmpty(t *testing.T) {
	res, err := Scan(filepath.Join(t.TempDir(), "nope"), NewMatcher(nil), 0)
	if err != nil || len(res.Files) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// Regression (simulation seed 1008): a user write landing between the last
// precondition check and the commit was silently overwritten.
func TestWriteRaceAbsentTargetCreatedConcurrently(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	beforeCommit = func(string) { os.WriteFile(p, []byte("user"), 0o644) }
	defer func() { beforeCommit = nil }()
	if _, err := Write(p, []byte("sync"), WriteOpts{ExpectAbsent: true}); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "user" {
		t.Fatalf("user write lost: %q", b)
	}
}

func TestWriteRaceInPlaceEditDuringOverwrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	writeFile(t, p, "old")
	beforeCommit = func(string) { os.WriteFile(p, []byte("user edit"), 0o644) } // same inode
	defer func() { beforeCommit = nil }()
	if _, err := Write(p, []byte("sync"), WriteOpts{ExpectHash: Hash([]byte("old"))}); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "user edit" {
		t.Fatalf("user edit lost: %q", b)
	}
}

func TestWriteRaceRenameSaveDuringOverwrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	writeFile(t, p, "old")
	beforeCommit = func(string) { // editor-style atomic save
		writeFile(t, filepath.Join(dir, "editor.tmp"), "editor save")
		os.Rename(filepath.Join(dir, "editor.tmp"), p)
	}
	defer func() { beforeCommit = nil }()
	if _, err := Write(p, []byte("sync"), WriteOpts{ExpectHash: Hash([]byte("old"))}); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "editor save" {
		t.Fatalf("editor save lost: %q", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}
