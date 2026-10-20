// Package config locates Enigma's files on a machine and stores the
// machine's identity and peers (SPEC §10, §11).
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Paths are the two roots Enigma uses on a machine.
type Paths struct {
	Config string // local settings
	Data   string // state, trash, backups, sockets, identity
}

// Resolve applies ENIGMA_HOME, then XDG_CONFIG_HOME / XDG_DATA_HOME, then
// the defaults under home.
func Resolve(getenv func(string) string, home string) Paths {
	if h := getenv("ENIGMA_HOME"); h != "" {
		return Paths{Config: h, Data: h}
	}
	cfg := filepath.Join(home, ".config")
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		cfg = x
	}
	data := filepath.Join(home, ".local", "share")
	if x := getenv("XDG_DATA_HOME"); x != "" {
		data = x
	}
	return Paths{Config: filepath.Join(cfg, "enigma"), Data: filepath.Join(data, "enigma")}
}

// Default resolves paths for the current user.
func Default() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	return Resolve(os.Getenv, home), nil
}

func (p Paths) APISocket() string  { return p.socket("enigma") }
func (p Paths) PeerSocket() string { return p.socket("peer") }

// socket keeps unix socket paths under the ~104-byte limit of macOS by
// falling back to a short, deterministic path in /tmp.
func (p Paths) socket(name string) string {
	s := filepath.Join(p.Data, name+".sock")
	if len(s) < 100 {
		return s
	}
	h := sha256.Sum256([]byte(p.Data))
	return filepath.Join("/tmp", fmt.Sprintf("enigma-%d-%s-%s.sock", os.Getuid(), hex.EncodeToString(h[:4]), name))
}

// ---- identity ---------------------------------------------------------------

var ErrNotInitialized = errors.New("this machine is not in a group yet: run `enigma init` (first machine) or `enigma join <ssh-alias>`")

type Identity struct {
	MachineID string `json:"machine_id"`
	GroupID   string `json:"group_id"`
	Name      string `json:"name"`
}

func (p Paths) identityPath() string { return filepath.Join(p.Data, "identity.json") }

func LoadIdentity(p Paths) (Identity, error) {
	var id Identity
	if err := readJSON(p.identityPath(), &id); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Identity{}, ErrNotInitialized
		}
		return Identity{}, err
	}
	return id, nil
}

// Init creates a new group with this machine as its first member.
func Init(p Paths, name string) (Identity, error) {
	if id, err := LoadIdentity(p); err == nil {
		return id, fmt.Errorf("this machine already belongs to group %s", id.GroupID)
	}
	id := Identity{MachineID: randomID(), GroupID: randomID(), Name: name}
	return id, writeJSON(p.identityPath(), id)
}

// Join makes this machine a member of groupID. Re-joining the same group
// is a no-op; joining another group is refused.
func Join(p Paths, groupID, name string) (Identity, error) {
	if id, err := LoadIdentity(p); err == nil {
		if id.GroupID != groupID {
			return id, fmt.Errorf("this machine already belongs to another group (%s)", id.GroupID)
		}
		return id, nil
	}
	id := Identity{MachineID: randomID(), GroupID: groupID, Name: name}
	return id, writeJSON(p.identityPath(), id)
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ---- peers ------------------------------------------------------------------

// Peer is a machine this one dials (the M2 stopgap, SPEC §11).
type Peer struct {
	Name      string `json:"name"`
	Target    string `json:"target"`               // ssh destination
	RemoteCmd string `json:"remote_cmd,omitempty"` // enigma on the peer, default "enigma"
}

func (p Paths) peersPath() string { return filepath.Join(p.Data, "peers.json") }

func LoadPeers(p Paths) ([]Peer, error) {
	var peers []Peer
	if err := readJSON(p.peersPath(), &peers); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return peers, nil
}

// AddPeer adds peer, or updates the one with the same target.
func AddPeer(p Paths, peer Peer) error {
	peers, err := LoadPeers(p)
	if err != nil {
		return err
	}
	for i := range peers {
		if peers[i].Target == peer.Target {
			peers[i] = peer
			return writeJSON(p.peersPath(), peers)
		}
	}
	return writeJSON(p.peersPath(), append(peers, peer))
}

func RemovePeer(p Paths, name string) error {
	peers, err := LoadPeers(p)
	if err != nil {
		return err
	}
	for i := range peers {
		if peers[i].Name == name {
			return writeJSON(p.peersPath(), append(peers[:i], peers[i+1:]...))
		}
	}
	return fmt.Errorf("no peer named %q", name)
}

// ---- helpers ----------------------------------------------------------------

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
