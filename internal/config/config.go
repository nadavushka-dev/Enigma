// Package config locates Enigma's files on a machine and stores its
// identity, the records it joined with, and its local settings (SPEC §10, §11).
package config

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/nadavushka-dev/enigma/internal/proto"
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

var ErrNotInitialized = errors.New("this machine is not in a group yet: run `enigma init` (first machine), or add it from a member with `enigma machine add <ssh-alias>`")

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

// Leave forgets the group: identity, state and history go; trash and
// backups (the user's own data) stay.
func Leave(p Paths) error {
	for _, f := range []string{p.identityPath(), p.bootstrapPath(), filepath.Join(p.Data, "state.json")} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return os.RemoveAll(filepath.Join(p.Data, "history"))
}

// Join makes this machine a member of a group, as the identity the adding
// machine chose for it, and leaves members (the group's machine records)
// for the daemon to start from (SPEC §11). Joining again as the same
// machine is a no-op; anything else is refused.
func Join(p Paths, id Identity, members []proto.MachineRecord) error {
	if cur, err := LoadIdentity(p); err == nil {
		switch {
		case cur.GroupID != id.GroupID:
			return fmt.Errorf("this machine already belongs to another group (%s)", cur.GroupID)
		case cur.MachineID != id.MachineID:
			return fmt.Errorf("this machine is already in this group as %s", cur.Name)
		}
	} else if !errors.Is(err, ErrNotInitialized) {
		return err
	}
	if err := writeJSON(p.bootstrapPath(), members); err != nil {
		return err
	}
	return writeJSON(p.identityPath(), id)
}

// NewID is a random machine or group ID.
func NewID() string { return randomID() }

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (p Paths) bootstrapPath() string { return filepath.Join(p.Data, "join.json") }

// LoadBootstrap returns the machine records left by Join, if any.
func LoadBootstrap(p Paths) ([]proto.MachineRecord, error) {
	var rs []proto.MachineRecord
	if err := readJSON(p.bootstrapPath(), &rs); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return rs, nil
}

// ClearBootstrap drops them once the engine holds them.
func ClearBootstrap(p Paths) { os.Remove(p.bootstrapPath()) }

// ---- local settings (config.toml, never synced) ----------------------------

type Local struct {
	// Notifications turns desktop notifications on or off (SPEC §12.3);
	// on unless config.toml says `notifications = false`.
	Notifications bool `toml:"notifications"`
	// Aliases override how this machine reaches a member, by member name.
	Aliases map[string]string `toml:"aliases"`
}

func (p Paths) localPath() string { return filepath.Join(p.Config, "config.toml") }

func LoadLocal(p Paths) (Local, error) {
	l := Local{Notifications: true, Aliases: map[string]string{}}
	if _, err := toml.DecodeFile(p.localPath(), &l); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return l, fmt.Errorf("%s: %w", p.localPath(), err)
	}
	if l.Aliases == nil {
		l.Aliases = map[string]string{}
	}
	return l, nil
}

// SetAlias sets (or, with alias "", clears) the local override for name.
func SetAlias(p Paths, name, alias string) error {
	l, err := LoadLocal(p)
	if err != nil {
		return err
	}
	if alias == "" {
		delete(l.Aliases, name)
	} else {
		l.Aliases[name] = alias
	}
	var buf bytes.Buffer
	buf.WriteString("# Enigma local settings for this machine (never synced).\n")
	if err := toml.NewEncoder(&buf).Encode(l); err != nil {
		return err
	}
	if err := os.MkdirAll(p.Config, 0o700); err != nil {
		return err
	}
	tmp := p.localPath() + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.localPath())
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
