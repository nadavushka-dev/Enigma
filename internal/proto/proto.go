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
	Entries     Type = "entries"      // fleet state: entry records
	Summary     Type = "summary"      // catch-up: path -> current version
	SummaryDone Type = "summary_done" // end of the catch-up summary
	Announce    Type = "announce"     // a new current version exists
	Fetch       Type = "fetch"        // send me the content of path
	File        Type = "file"         // version + content
	Machines    Type = "machines"     // fleet state: machine records
	Upgrade     Type = "upgrade"      // a newer binary for the receiver (SPEC §11)
	Ping        Type = "ping"         // heartbeat: a link silent for 3 intervals is dead
	Settings    Type = "settings"     // fleet state: the fleet's limits (SPEC §8)
)

type HelloBody struct {
	FleetID   string `json:"group_id"` // wire key kept from v0.2 so an old daemon still takes the upgrade
	MachineID string `json:"machine_id"`
	Name      string `json:"name"`
	Proto     int    `json:"proto"`
	Version   string `json:"version,omitempty"`  // Enigma version; must match to sync (SPEC §11)
	Platform  string `json:"platform,omitempty"` // os/arch, so a newer peer can send the right binary
}

// MachineRecord is one member in the replicated fleet state (SPEC §11).
// The record with the highest (Clock, By) wins.
type MachineRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Alias   string `json:"alias,omitempty"`  // ssh alias it was added under
	Target  string `json:"target,omitempty"` // ssh://user@host:port, from ssh -G on the adding machine
	Bin     string `json:"bin,omitempty"`    // its enigma binary
	Clock   uint64 `json:"clock"`
	By      string `json:"by"`
	Removed bool   `json:"removed,omitempty"`
}

func (r MachineRecord) Newer(o MachineRecord) bool {
	if r.Clock != o.Clock {
		return r.Clock > o.Clock
	}
	return r.By > o.By
}

// SettingsRecord holds the fleet's settings (SPEC §6.4, §8). The record
// with the highest (Clock, By) wins.
type SettingsRecord struct {
	MaxFileSize int64  `json:"max_file_size"`
	MaxFiles    int    `json:"max_files"`
	Clock       uint64 `json:"clock"`
	By          string `json:"by"`
}

func (r SettingsRecord) Newer(o SettingsRecord) bool {
	if r.Clock != o.Clock {
		return r.Clock > o.Clock
	}
	return r.By > o.By
}

// UpgradeBody describes the binary in an Upgrade frame's body, or why the
// sender has none for the receiver's platform.
type UpgradeBody struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
	Hash     string `json:"hash,omitempty"`
	Error    string `json:"error,omitempty"`
}

// EntryRecord is one entry in the replicated fleet state (SPEC §6.4).
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
	Type     Type                       `json:"type"`
	Hello    *HelloBody                 `json:"hello,omitempty"`
	Entries  []EntryRecord              `json:"entries,omitempty"`
	Machines []MachineRecord            `json:"machines,omitempty"`
	Settings *SettingsRecord            `json:"settings,omitempty"`
	Upgrade  *UpgradeBody               `json:"upgrade,omitempty"`
	Summary  map[string]version.Version `json:"summary,omitempty"`
	Path     string                     `json:"path,omitempty"`
	Version  *version.Version           `json:"version,omitempty"`
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
