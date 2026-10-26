package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/enigma/internal/proto"
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
	if len(id.MachineID) < 16 || len(id.FleetID) < 16 || id.Name != "mac" || id.MachineID == id.FleetID {
		t.Fatalf("identity = %+v", id)
	}
	if _, err := Init(p, "mac"); err == nil {
		t.Fatal("second init must fail")
	}
	got, err := LoadIdentity(p)
	if err != nil || got != id {
		t.Fatalf("load = %+v, %v", got, err)
	}
	// Joining as the same machine of the same fleet is fine; anything else is not.
	if err := Join(p, id, nil); err != nil {
		t.Fatalf("rejoin same fleet: %v", err)
	}
	if err := Join(p, Identity{MachineID: id.MachineID, FleetID: "another-fleet", Name: "mac"}, nil); err == nil {
		t.Fatal("joining a different fleet must fail")
	}
	if err := Join(p, Identity{MachineID: NewID(), FleetID: id.FleetID, Name: "mac"}, nil); err == nil {
		t.Fatal("joining again as another machine must fail")
	}
}

func TestJoinWritesIdentityAndBootstrap(t *testing.T) {
	p := Paths{Data: t.TempDir()}
	id := Identity{MachineID: NewID(), FleetID: "fleet-123", Name: "box"}
	members := []proto.MachineRecord{{ID: "a", Name: "mac", Clock: 1, By: "a"}, {ID: id.MachineID, Name: "box", Target: "ssh://me@box:22", Clock: 2, By: "a"}}
	if err := Join(p, id, members); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadIdentity(p); err != nil || got != id {
		t.Fatalf("identity %+v %v", got, err)
	}
	got, err := LoadBootstrap(p)
	if err != nil || !reflect.DeepEqual(got, members) {
		t.Fatalf("bootstrap %+v %v", got, err)
	}
	ClearBootstrap(p)
	if got, err := LoadBootstrap(p); err != nil || len(got) != 0 {
		t.Fatalf("after clear %+v %v", got, err)
	}
}

func TestLocalAliasOverrides(t *testing.T) {
	p := Paths{Config: filepath.Join(t.TempDir(), "cfg")}
	l, err := LoadLocal(p)
	if err != nil || len(l.Aliases) != 0 {
		t.Fatalf("empty: %+v %v", l, err)
	}
	if err := SetAlias(p, "devbox", "devbox-vpn"); err != nil {
		t.Fatal(err)
	}
	if err := SetAlias(p, "laptop", "lap"); err != nil {
		t.Fatal(err)
	}
	if err := SetAlias(p, "laptop", ""); err != nil { // clears
		t.Fatal(err)
	}
	l, _ = LoadLocal(p)
	if !reflect.DeepEqual(l.Aliases, map[string]string{"devbox": "devbox-vpn"}) {
		t.Fatalf("aliases %+v", l.Aliases)
	}
	b, _ := os.ReadFile(filepath.Join(p.Config, "config.toml"))
	if !strings.Contains(string(b), "[aliases]") {
		t.Fatalf("config.toml:\n%s", b)
	}
}

func TestLocalNotificationsDefaultOnCanBeTurnedOff(t *testing.T) {
	p := Paths{Config: filepath.Join(t.TempDir(), "cfg")}
	l, err := LoadLocal(p)
	if err != nil || !l.Notifications {
		t.Fatalf("no config.toml: notifications should default on: %+v %v", l, err)
	}
	os.MkdirAll(p.Config, 0o700)
	os.WriteFile(filepath.Join(p.Config, "config.toml"), []byte("notifications = false\n"), 0o600)
	if l, err = LoadLocal(p); err != nil || l.Notifications {
		t.Fatalf("notifications = false ignored: %+v %v", l, err)
	}
	if err := SetAlias(p, "devbox", "vpn"); err != nil {
		t.Fatal(err)
	}
	if l, _ = LoadLocal(p); l.Notifications {
		t.Fatal("SetAlias dropped notifications = false")
	}
}
