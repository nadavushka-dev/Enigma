package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func readlink(t *testing.T, p string) string {
	t.Helper()
	s, err := os.Readlink(p)
	if err != nil {
		return "<not a link>"
	}
	return s
}

func TestStatReportsLinkWithoutFollowing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "target"), "content")
	os.Symlink("target", filepath.Join(dir, "rel"))
	os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "dangling"))
	for name, want := range map[string]string{"rel": "target", "dangling": filepath.Join(dir, "nowhere")} {
		info, err := Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Kind != Other || !info.Link || info.Target != want || info.Hash != "" || info.Mtime == 0 {
			t.Errorf("%s: %+v, want an unfollowed link to %s", name, info, want)
		}
	}
	if LinkHash("x") == Hash([]byte("x")) || LinkHash("x") == LinkHash("y") {
		t.Fatal("link hashes must be domain-separated and differ by target")
	}
}

func TestWriteLinkAtomicNeverClobbersFileOrDir(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "l")

	// absent: created, parents too
	if err := WriteLink(p, "a", LinkOpts{ExpectAbsent: true}); err != nil {
		t.Fatal(err)
	}
	if readlink(t, p) != "a" {
		t.Fatal("not created")
	}
	// absent expected, but a link is there
	if err := WriteLink(p, "b", LinkOpts{ExpectAbsent: true}); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	// retarget: expect the old target
	if err := WriteLink(p, "b", LinkOpts{ExpectLink: "wrong"}); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	if err := WriteLink(p, "b", LinkOpts{ExpectLink: "a"}); err != nil || readlink(t, p) != "b" {
		t.Fatalf("retarget: %v, %s", err, readlink(t, p))
	}

	// a regular file: replaced only when it holds the expected content
	f := filepath.Join(dir, "f")
	writeFile(t, f, "keep")
	if err := WriteLink(f, "x", LinkOpts{ExpectAbsent: true}); !errors.Is(err, ErrChanged) {
		t.Fatalf("over a file expecting absence: %v", err)
	}
	if err := WriteLink(f, "x", LinkOpts{ExpectHash: Hash([]byte("other"))}); !errors.Is(err, ErrChanged) {
		t.Fatalf("over a changed file: %v", err)
	}
	if b, _ := os.ReadFile(f); string(b) != "keep" {
		t.Fatal("file clobbered")
	}
	if err := WriteLink(f, "x", LinkOpts{ExpectHash: Hash([]byte("keep"))}); err != nil || readlink(t, f) != "x" {
		t.Fatalf("over the expected file: %v", err)
	}

	// a folder is never replaced, whatever is expected
	d := filepath.Join(dir, "d")
	writeFile(t, filepath.Join(d, "inside"), "x")
	for _, o := range []LinkOpts{{ExpectAbsent: true}, {ExpectLink: "x"}, {ExpectHash: Hash([]byte("x"))}} {
		if err := WriteLink(d, "x", o); !errors.Is(err, ErrNotRegular) {
			t.Fatalf("over a folder (%+v): %v", o, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(d, "inside")); string(b) != "x" {
		t.Fatal("folder clobbered")
	}
	// no temp links left behind
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if len(e.Name()) > len(TempPrefix) && e.Name()[:len(TempPrefix)] == TempPrefix {
			t.Fatalf("temp left: %s", e.Name())
		}
	}
}

func TestWriteLinkKeepsAnEditMadeAtTheLastMoment(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	writeFile(t, f, "one")
	beforeCommit = func(string) { os.WriteFile(f, []byte("edited"), 0o644) }
	defer func() { beforeCommit = nil }()
	if err := WriteLink(f, "x", LinkOpts{ExpectHash: Hash([]byte("one"))}); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	if b, _ := os.ReadFile(f); string(b) != "edited" {
		t.Fatalf("the edit was lost: %q", b)
	}
}

func TestMoveLinkToMovesTheLinkNotItsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	writeFile(t, target, "content")
	l := filepath.Join(dir, "l")
	os.Symlink(target, l)
	if _, err := MoveLinkTo(l, filepath.Join(dir, "trash", "l"), "wrong"); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	if _, err := MoveLinkTo(target, filepath.Join(dir, "trash", "t"), target); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("moving a file as a link: %v", err)
	}
	got, err := MoveLinkTo(l, filepath.Join(dir, "trash", "l"), target)
	if err != nil {
		t.Fatal(err)
	}
	if readlink(t, got) != target {
		t.Fatal("the link was not moved as is")
	}
	if b, _ := os.ReadFile(target); string(b) != "content" {
		t.Fatal("target changed")
	}
	if _, err := os.Lstat(l); !os.IsNotExist(err) {
		t.Fatal("link still in place")
	}
}

func TestCountFilesCountsLinksWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a"), "x")
	os.Symlink("a", filepath.Join(dir, "l1"))
	os.Symlink("nowhere", filepath.Join(dir, "l2"))
	m := NewMatcher(nil)
	if n, _ := CountFiles(dir, m, 0, 100); n != 1 {
		t.Fatalf("CountFiles = %d, want 1 (links not counted by default)", n)
	}
	if n, _ := CountSynced(dir, m, 0, 100, true); n != 3 {
		t.Fatalf("CountSynced with links = %d, want 3", n)
	}
	links, _ := ListLinks(dir, m)
	if len(links) != 2 {
		t.Fatalf("ListLinks = %v", links)
	}
	res, _ := Scan(dir, m, 0)
	if len(res.Links) != 2 || len(res.Skipped) != 2 || len(res.Files) != 1 {
		t.Fatalf("Scan = %+v", res)
	}
}
