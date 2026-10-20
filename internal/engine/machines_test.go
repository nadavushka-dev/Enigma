package engine

import (
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

// tryLink is link that returns both sides' errors instead of failing.
func tryLink(a, b *machine) (errA, errB error, cut func()) {
	c1, c2 := net.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errA = a.e.Connect(c1, true) }()
	go func() { defer wg.Done(); errB = b.e.Connect(c2, false) }()
	wg.Wait()
	return errA, errB, func() { c1.Close(); c2.Close() }
}

func (m *machine) machine(id string) (proto.MachineRecord, bool) {
	for _, r := range m.e.Machines() {
		if r.ID == id {
			return r, true
		}
	}
	return proto.MachineRecord{}, false
}

func TestS11_EveryMachineHasItsOwnRecord(t *testing.T) {
	a := newMachine(t, "a", func(o *Options) { o.Bin = "/opt/enigma" })
	r, ok := a.machine("id-a")
	if !ok || r.Name != "a" || r.Bin != "/opt/enigma" || r.Target != "" {
		t.Fatalf("own record %+v %v", r, ok)
	}
}

func TestS11_BootstrapRecordIsKeptNotReplaced(t *testing.T) {
	// A joined machine gets its full record from the machine that added it;
	// it must not overwrite it with a bare one of its own.
	full := proto.MachineRecord{ID: "id-b", Name: "devbox", Alias: "devbox", Target: "ssh://me@devbox:22", Bin: "~/.local/bin/enigma", Clock: 5, By: "id-a"}
	b := newMachine(t, "b", func(o *Options) { o.Bootstrap = []proto.MachineRecord{full} })
	if r, _ := b.machine("id-b"); r != full {
		t.Fatalf("got %+v", r)
	}
}

func TestS11_MachineRecordsReplicate(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	var mu sync.Mutex
	var seen []string
	c := newMachine(t, "c", func(o *Options) {
		o.OnMachines = func(rs []proto.MachineRecord) {
			mu.Lock()
			defer mu.Unlock()
			for _, r := range rs {
				seen = append(seen, r.Name)
			}
		}
	})
	link(t, a, b)
	link(t, b, c)
	if err := a.e.SetMachine(proto.MachineRecord{ID: "id-x", Name: "x", Alias: "x", Target: "ssh://u@x:22"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "c knows x (through b)", func() bool { _, ok := c.machine("id-x"); return ok })
	eventually(t, "c was told", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(strings.Join(seen, ","), "x")
	})
}

func TestS11_RenameIsLastWriterWins(t *testing.T) {
	a, b := newMachine(t, "a"), newMachine(t, "b")
	link(t, a, b)
	eventually(t, "b knows a", func() bool { _, ok := b.machine("id-a"); return ok })
	r, _ := b.machine("id-a")
	r.Name = "laptop"
	b.e.SetMachine(r)
	eventually(t, "a sees its new name", func() bool { r, _ := a.machine("id-a"); return r.Name == "laptop" })
}

func TestS11_RemovedMachineIsRefusedAndUnlinked(t *testing.T) {
	a, b, c := newMachine(t, "a"), newMachine(t, "b"), newMachine(t, "c")
	link(t, a, b)
	link(t, a, c)
	eventually(t, "a knows c", func() bool { _, ok := a.machine("id-c"); return ok })
	r, _ := a.machine("id-c")
	r.Removed = true
	a.e.SetMachine(r)
	eventually(t, "c's link dropped", func() bool {
		for _, p := range a.e.Status().Peers {
			if p.ID == "id-c" {
				return false
			}
		}
		return true
	})
	eventually(t, "b knows c is removed", func() bool { r, _ := b.machine("id-c"); return r.Removed })
	errB, _, _ := tryLink(b, c)
	if errB == nil || !strings.Contains(errB.Error(), "removed") {
		t.Fatalf("link to a removed machine: %v", errB)
	}
}

// ---- one version everywhere (SPEC §11) -----------------------------------------

type installs struct {
	mu   sync.Mutex
	got  []string // version:content
	fail error
}

func (in *installs) list() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.got...)
}

func versioned(v string, binFor map[string]string, in *installs) func(*Options) {
	return func(o *Options) {
		o.Version, o.Platform = v, "linux/arm64"
		o.BinaryFor = func(platform string) ([]byte, error) {
			b, ok := binFor[platform]
			if !ok {
				return nil, errors.New("no " + platform + " binary; run make dist")
			}
			return []byte(b), nil
		}
		if in != nil {
			o.InstallBinary = func(version string, bin []byte) error {
				in.mu.Lock()
				defer in.mu.Unlock()
				in.got = append(in.got, version+":"+string(bin))
				return in.fail
			}
		}
	}
}

func TestS11_NewerSideUpgradesOlderOverTheLink(t *testing.T) {
	var old installs
	a := newMachine(t, "a", versioned("v1.1.0", map[string]string{"linux/arm64": "BINARY-1.1"}, nil))
	b := newMachine(t, "b", versioned("v1.0.0", nil, &old))
	a.write("f.md", "x")
	a.add("f.md")
	errA, errB, _ := tryLink(b, a) // the older side dialed: still upgraded
	if errA == nil || errB == nil {
		t.Fatalf("a mismatched link must not attach: %v / %v", errA, errB)
	}
	if got := old.list(); len(got) != 1 || got[0] != "v1.1.0:BINARY-1.1" {
		t.Fatalf("installed %v", got)
	}
	time.Sleep(100 * time.Millisecond)
	if b.read("f.md") != "<missing>" {
		t.Fatal("a mismatched link synced files")
	}
}

func TestS11_NewerSideIsNeverDowngraded(t *testing.T) {
	var newer installs
	a := newMachine(t, "a", versioned("v1.1.0", nil, &newer))
	b := newMachine(t, "b", versioned("v1.0.0", map[string]string{"linux/arm64": "OLD"}, nil))
	tryLink(a, b)
	tryLink(b, a)
	if got := newer.list(); len(got) != 0 {
		t.Fatalf("newer machine installed %v", got)
	}
}

func TestS11_UpgradeRejectsWrongHashOrVersion(t *testing.T) {
	var got installs
	b := newMachine(t, "b", versioned("v1.0.0", nil, &got))
	for _, up := range []proto.UpgradeBody{
		{Version: "v1.1.0", Platform: "linux/arm64", Hash: "bad"},
		{Version: "v0.9.0", Platform: "linux/arm64", Hash: fsx.Hash([]byte("BIN"))},
		{Version: "v1.1.0", Platform: "darwin/arm64", Hash: fsx.Hash([]byte("BIN"))},
	} {
		if err := b.e.acceptUpgrade(up, []byte("BIN")); err == nil {
			t.Errorf("accepted %+v", up)
		}
	}
	if len(got.list()) != 0 {
		t.Fatalf("installed %v", got.list())
	}
}

func TestS11_NoBinaryForPlatformIsReported(t *testing.T) {
	a := newMachine(t, "a", versioned("v1.1.0", nil, nil))
	b := newMachine(t, "b", versioned("v1.0.0", nil, &installs{}))
	errA, errB, _ := tryLink(a, b)
	for _, err := range []error{errA, errB} {
		if err == nil || !strings.Contains(err.Error(), "make dist") {
			t.Fatalf("errors %v / %v", errA, errB)
		}
	}
}

func TestS11_UnorderedVersionsDoNotLink(t *testing.T) {
	var in installs
	a := newMachine(t, "a", versioned("dev", map[string]string{"linux/arm64": "X"}, &in))
	b := newMachine(t, "b", versioned("v1.0.0", map[string]string{"linux/arm64": "Y"}, &in))
	errA, _, _ := tryLink(a, b)
	if errA == nil || !strings.Contains(errA.Error(), "version") {
		t.Fatalf("err %v", errA)
	}
	if len(in.list()) != 0 {
		t.Fatalf("installed %v", in.list())
	}
}
