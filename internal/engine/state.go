package engine

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/proto"
	"github.com/nadavushka-dev/enigma/internal/version"
)

type fileState struct {
	V version.Version `json:"v"`
	// Materialized: this machine has written or seen this version on disk.
	// A missing file that was never materialized here is not a delete (§6.5).
	Materialized bool `json:"materialized,omitempty"`
	// Anc is the hash graph nearest V, for merge bases (SPEC §7 step 3).
	Anc version.Graph `json:"anc,omitempty"`
}

// state is everything a machine persists about the fleet (SPEC §10).
type state struct {
	Counter  uint64                         `json:"counter"` // this machine's vector counter, global across files
	Lamport  uint64                         `json:"lamport"` // fleet state clock
	Entries  map[string]proto.EntryRecord   `json:"entries"`
	Machines map[string]proto.MachineRecord `json:"machines"`
	Groups   map[string]proto.GroupRecord   `json:"groups,omitempty"`   // by ID (SPEC §9)
	Settings *proto.SettingsRecord          `json:"settings,omitempty"` // nil: the defaults
	Files    map[string]*fileState          `json:"files"`
	// Stamps: the last hash taken of a file on disk, valid while its
	// lstat stamp is unchanged (SPEC §6.1). Keyed by path from ~.
	Stamps map[string]*hashStamp `json:"stamps,omitempty"`
}

type hashStamp struct {
	fsx.Stamp
	Hash string `json:"hash"`
}

func newState() *state {
	return &state{Entries: map[string]proto.EntryRecord{}, Machines: map[string]proto.MachineRecord{}, Groups: map[string]proto.GroupRecord{}, Files: map[string]*fileState{}, Stamps: map[string]*hashStamp{}}
}

func loadState(p string) (*state, error) {
	s := newState()
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Entries == nil {
		s.Entries = map[string]proto.EntryRecord{}
	}
	if s.Machines == nil {
		s.Machines = map[string]proto.MachineRecord{}
	}
	if s.Groups == nil {
		s.Groups = map[string]proto.GroupRecord{}
	}
	for p, r := range s.Entries {
		if r.Group == "" { // a v0.2 entry: it is in main (SPEC §9)
			r.Group = proto.MainGroup
			s.Entries[p] = r
		}
	}
	if s.Files == nil {
		s.Files = map[string]*fileState{}
	}
	if s.Stamps == nil {
		s.Stamps = map[string]*hashStamp{}
	}
	return s, nil
}

func (s *state) save(p string) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
