package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveDefaults(t *testing.T) {
	p := Resolve(envOf(nil), "/home/u")
	if p.Config != "/home/u/.config/enigma" || p.Data != "/home/u/.local/share/enigma" {
		t.Fatalf("paths = %+v", p)
	}
}

func TestResolveXDG(t *testing.T) {
	p := Resolve(envOf(map[string]string{"XDG_CONFIG_HOME": "/x/cfg", "XDG_DATA_HOME": "/x/data"}), "/home/u")
	if p.Config != "/x/cfg/enigma" || p.Data != "/x/data/enigma" {
		t.Fatalf("paths = %+v", p)
	}
}

func TestResolveEnigmaHomeWins(t *testing.T) {
	p := Resolve(envOf(map[string]string{"ENIGMA_HOME": "/t/e", "XDG_DATA_HOME": "/x"}), "/home/u")
	if p.Config != "/t/e" || p.Data != "/t/e" {
		t.Fatalf("paths = %+v", p)
	}
}

func TestSocketsAreShortAndDistinct(t *testing.T) {
	short := Paths{Data: "/home/u/.local/share/enigma"}
	if short.APISocket() != "/home/u/.local/share/enigma/enigma.sock" || short.PeerSocket() != "/home/u/.local/share/enigma/peer.sock" {
		t.Fatalf("short paths changed: %s %s", short.APISocket(), short.PeerSocket())
	}
	long := Paths{Data: "/" + strings.Repeat("deep/", 30) + "enigma"}
	a, p := long.APISocket(), long.PeerSocket()
	if len(a) >= 100 || len(p) >= 100 || a == p {
		t.Fatalf("fallback sockets: %s %s", a, p)
	}
	other := Paths{Data: "/" + strings.Repeat("deep/", 30) + "other"}
	if other.APISocket() == a {
		t.Fatal("different data dirs must not share a socket")
	}
	if long.APISocket() != a {
		t.Fatal("fallback must be deterministic (CLI and daemon must agree)")
	}
}

func TestIdentityInitLoadJoin(t *testing.T) {
	p := Paths{Data: t.TempDir()}
	if _, err := LoadIdentity(p); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("err = %v", err)
	}
	id, err := Init(p, "mac")
	if err != nil {
		t.Fatal(err)
	}
	if len(id.MachineID) < 16 || len(id.GroupID) < 16 || id.Name != "mac" || id.MachineID == id.GroupID {
		t.Fatalf("identity = %+v", id)
	}
	if _, err := Init(p, "mac"); err == nil {
		t.Fatal("second init must fail")
	}
	got, err := LoadIdentity(p)
	if err != nil || got != id {
		t.Fatalf("load = %+v, %v", got, err)
	}
	// Joining the group you already belong to is fine; another group is not.
	if _, err := Join(p, id.GroupID, "mac"); err != nil {
		t.Fatalf("rejoin same group: %v", err)
	}
	if _, err := Join(p, "another-group", "mac"); err == nil {
		t.Fatal("joining a different group must fail")
	}
}

func TestJoinFreshMachine(t *testing.T) {
	p := Paths{Data: t.TempDir()}
	id, err := Join(p, "group-123", "box")
	if err != nil || id.GroupID != "group-123" || id.MachineID == "" {
		t.Fatalf("join = %+v, %v", id, err)
	}
}

func TestPeers(t *testing.T) {
	p := Paths{Data: filepath.Join(t.TempDir(), "nested")}
	peers, err := LoadPeers(p)
	if err != nil || len(peers) != 0 {
		t.Fatalf("empty load: %v %v", peers, err)
	}
	if err := AddPeer(p, Peer{Name: "devbox", Target: "devbox"}); err != nil {
		t.Fatal(err)
	}
	if err := AddPeer(p, Peer{Name: "devbox", Target: "devbox", RemoteCmd: "~/bin/enigma"}); err != nil {
		t.Fatal(err)
	}
	if err := AddPeer(p, Peer{Name: "laptop", Target: "me@laptop"}); err != nil {
		t.Fatal(err)
	}
	peers, _ = LoadPeers(p)
	if len(peers) != 2 || peers[0].RemoteCmd != "~/bin/enigma" {
		t.Fatalf("peers = %+v (same target must update, not duplicate)", peers)
	}
	if err := RemovePeer(p, "devbox"); err != nil {
		t.Fatal(err)
	}
	if peers, _ = LoadPeers(p); len(peers) != 1 || peers[0].Name != "laptop" {
		t.Fatalf("after remove: %+v", peers)
	}
	if err := RemovePeer(p, "nope"); err == nil {
		t.Fatal("removing an unknown peer must fail")
	}
}
