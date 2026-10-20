package proto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"testing"
	"testing/iotest"

	"github.com/nadavushka-dev/enigma/internal/version"
)

func roundTrip(t *testing.T, msgs ...*Msg) []*Msg {
	t.Helper()
	var buf bytes.Buffer
	for _, m := range msgs {
		if err := Write(&buf, m); err != nil {
			t.Fatal(err)
		}
	}
	// One byte at a time: framing must not depend on read sizes.
	r := iotest.OneByteReader(&buf)
	var out []*Msg
	for range msgs {
		m, err := Read(r)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	if _, err := Read(r); !errors.Is(err, io.EOF) {
		t.Fatalf("trailing read err = %v, want EOF", err)
	}
	return out
}

func TestRoundTripAllTypes(t *testing.T) {
	v := version.Version{Hash: "h", Vector: version.Vector{"a": 1}, Mtime: 42, By: "a", ByName: "mac", Origin: version.Seed}
	msgs := []*Msg{
		{Type: Hello, Hello: &HelloBody{GroupID: "g", MachineID: "a", Name: "mac", Proto: Version}},
		{Type: Entries, Entries: []EntryRecord{{Path: ".claude/skills", Dir: true, Ignore: []string{"*.log"}, Clock: 3, By: "a"}}},
		{Type: Announce, Path: ".claude/CLAUDE.md", Version: &v},
		{Type: Fetch, Path: ".claude/CLAUDE.md"},
		{Type: File, Path: ".claude/CLAUDE.md", Version: &v, Ancestry: []version.Node{{Hash: "h", Parents: []string{"p"}}, {Hash: "p"}}, Content: []byte("# rules\x00binary-safe")},
		{Type: Summary, Summary: map[string]version.Version{"x": v}},
		{Type: SummaryDone},
	}
	got := roundTrip(t, msgs...)
	for i := range msgs {
		if !reflect.DeepEqual(got[i], msgs[i]) {
			t.Errorf("msg %d:\n got %+v\nwant %+v", i, got[i], msgs[i])
		}
	}
}

func TestEmptyContentIsNotNilConfusion(t *testing.T) {
	got := roundTrip(t, &Msg{Type: File, Path: "empty", Content: []byte{}})
	if got[0].Content == nil || len(got[0].Content) != 0 {
		t.Fatalf("empty file content must survive as empty, got %#v", got[0].Content)
	}
}

func TestRejectsOversizedFrames(t *testing.T) {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:], MaxHeader+1)
	if _, err := Read(bytes.NewReader(hdr[:])); err == nil {
		t.Fatal("oversized header accepted")
	}
	binary.BigEndian.PutUint32(hdr[0:], 2)
	binary.BigEndian.PutUint32(hdr[4:], MaxBody+1)
	if _, err := Read(bytes.NewReader(append(hdr[:], '{', '}'))); err == nil {
		t.Fatal("oversized body accepted")
	}
}

func TestTruncatedFrameIsUnexpectedEOF(t *testing.T) {
	var buf bytes.Buffer
	Write(&buf, &Msg{Type: Fetch, Path: "p"})
	b := buf.Bytes()
	if _, err := Read(bytes.NewReader(b[:len(b)-1])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v", err)
	}
}
