// Package proto is the wire format between two Enigma daemons (SPEC §13):
// length-prefixed frames, each a JSON header followed by raw content bytes.
//
//	uint32 header length | uint32 body length | header JSON | body
package proto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/nadavushka-dev/enigma/internal/version"
)

// Version is the protocol version; bump it on any incompatible change.
const Version = 1

const (
	MaxHeader = 16 << 20
	MaxBody   = 64 << 20
)

type Type string

const (
	Hello       Type = "hello"
	Entries     Type = "entries"      // group state: entry records
	Summary     Type = "summary"      // catch-up: path -> current version
	SummaryDone Type = "summary_done" // end of the catch-up summary
	Announce    Type = "announce"     // a new current version exists
	Fetch       Type = "fetch"        // send me the content of path
	File        Type = "file"         // version + content
)

type HelloBody struct {
	GroupID   string `json:"group_id"`
	MachineID string `json:"machine_id"`
	Name      string `json:"name"`
	Proto     int    `json:"proto"`
}

// EntryRecord is one entry in the replicated group state (SPEC §6.4).
// The record with the highest (Clock, By) wins.
type EntryRecord struct {
	Path    string   `json:"path"` // relative to ~, slash-separated
	Dir     bool     `json:"dir,omitempty"`
	Ignore  []string `json:"ignore,omitempty"`
	Clock   uint64   `json:"clock"`
	By      string   `json:"by"`
	Removed bool     `json:"removed,omitempty"`
}

// Newer reports whether r supersedes o.
func (r EntryRecord) Newer(o EntryRecord) bool {
	if r.Clock != o.Clock {
		return r.Clock > o.Clock
	}
	return r.By > o.By
}

type Msg struct {
	Type    Type                       `json:"type"`
	Hello   *HelloBody                 `json:"hello,omitempty"`
	Entries []EntryRecord              `json:"entries,omitempty"`
	Summary map[string]version.Version `json:"summary,omitempty"`
	Path    string                     `json:"path,omitempty"`
	Version *version.Version           `json:"version,omitempty"`
	// Ancestry of Version (File only), for picking a merge base (SPEC §7).
	Ancestry []version.Node `json:"ancestry,omitempty"`
	HasBody  bool           `json:"body,omitempty"`
	Content  []byte         `json:"-"`
}

func Write(w io.Writer, m *Msg) error {
	h := *m // never mutate the caller's message
	h.HasBody = m.Content != nil
	hdr, err := json.Marshal(&h)
	if err != nil {
		return err
	}
	if len(hdr) > MaxHeader || len(m.Content) > MaxBody {
		return errors.New("frame too large")
	}
	buf := make([]byte, 8, 8+len(hdr)+len(m.Content))
	binary.BigEndian.PutUint32(buf[0:], uint32(len(hdr)))
	binary.BigEndian.PutUint32(buf[4:], uint32(len(m.Content)))
	buf = append(append(buf, hdr...), m.Content...)
	_, err = w.Write(buf)
	return err
}

// Read returns io.EOF only on a clean frame boundary.
func Read(r io.Reader) (*Msg, error) {
	var pre [8]byte
	if _, err := io.ReadFull(r, pre[:]); err != nil {
		return nil, err
	}
	hl, bl := binary.BigEndian.Uint32(pre[0:]), binary.BigEndian.Uint32(pre[4:])
	if hl > MaxHeader || bl > MaxBody {
		return nil, fmt.Errorf("frame too large (header %d, body %d)", hl, bl)
	}
	buf := make([]byte, int(hl)+int(bl))
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	m := &Msg{}
	if err := json.Unmarshal(buf[:hl], m); err != nil {
		return nil, fmt.Errorf("bad frame header: %w", err)
	}
	if m.HasBody {
		m.Content = buf[hl:]
	}
	m.HasBody = false
	return m, nil
}
