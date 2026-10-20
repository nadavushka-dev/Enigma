package engine

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

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

// state is everything a machine persists about the group (SPEC §10).
type state struct {
	Counter uint64                       `json:"counter"` // this machine's vector counter, global across files
	Lamport uint64                       `json:"lamport"` // group-state clock
	Entries map[string]proto.EntryRecord `json:"entries"`
	Files   map[string]*fileState        `json:"files"`
}

func newState() *state {
	return &state{Entries: map[string]proto.EntryRecord{}, Files: map[string]*fileState{}}
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
	if s.Files == nil {
		s.Files = map[string]*fileState{}
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
