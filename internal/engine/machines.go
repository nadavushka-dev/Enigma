package engine

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/nadavushka-dev/enigma/internal/buildinfo"
	"github.com/nadavushka-dev/enigma/internal/fsx"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

// upgradeTimeout bounds waiting for a newer peer's binary.
const upgradeTimeout = 2 * time.Minute

// ErrUpgrade: the link carried an upgrade instead of syncing (SPEC §11).
var ErrUpgrade = errors.New("version mismatch")

// ---- machine records (SPEC §11) ------------------------------------------------

// applyMachine merges a record (local when from is nil) into group state.
func (e *Engine) applyMachine(r proto.MachineRecord, from *peer) {
	if old, had := e.st.Machines[r.ID]; had && !r.Newer(old) {
		return
	}
	if r.Clock > e.st.Lamport {
		e.st.Lamport = r.Clock
	}
	e.st.Machines[r.ID] = r
	e.dirty = true
	e.broadcast(&proto.Msg{Type: proto.Machines, Machines: []proto.MachineRecord{r}}, from)
	if r.Removed {
		if p := e.peers[r.ID]; p != nil {
			e.log.Info("machine removed from the group; dropping its link", "peer", p.name)
			p.close()
		}
	}
	select {
	case e.machinesChanged <- struct{}{}:
	default:
	}
}

func (e *Engine) notifyMachines() {
	for {
		select {
		case <-e.quit:
			return
		case <-e.machinesChanged:
			rs := e.Machines()
			select {
			case <-e.quit:
				return
			default:
				e.o.OnMachines(rs)
			}
		}
	}
}

// SetMachine records r (add, rename, remove) in group state, as the latest
// change. Its Clock and By are set here.
func (e *Engine) SetMachine(r proto.MachineRecord) error {
	if r.ID == "" {
		return errors.New("machine record without an ID")
	}
	return e.do(func() {
		e.st.Lamport++
		r.Clock, r.By = e.st.Lamport, e.o.MachineID
		e.applyMachine(r, nil)
	})
}

// Machines returns every machine record, removed ones included, by name.
func (e *Engine) Machines() []proto.MachineRecord {
	var out []proto.MachineRecord
	e.do(func() {
		for _, r := range e.st.Machines {
			out = append(out, r)
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---- one version everywhere (SPEC §11) ------------------------------------------

// upgradeLink runs on a link whose peer has another version: the newer side
// sends its binary for the older side's platform, the older side installs
// it. Nothing syncs. It always returns an error (wrapping ErrUpgrade) and
// closes conn.
func (e *Engine) upgradeLink(conn io.ReadWriteCloser, h *proto.HelloBody) (err error) {
	defer conn.Close()
	defer func() { e.post(func() { e.event(EvUpgrade, "", h.Name, err.Error()) }) }()
	c, ok := buildinfo.Compare(e.o.Version, h.Version)
	if !ok || c == 0 {
		return fmt.Errorf("%w: %s runs %s, this machine %s, and neither is newer", ErrUpgrade, h.Name, h.Version, e.o.Version)
	}
	if c > 0 {
		up := proto.UpgradeBody{Version: e.o.Version, Platform: h.Platform}
		var bin []byte
		err := errors.New("no binary source")
		if e.o.BinaryFor != nil {
			bin, err = e.o.BinaryFor(h.Platform)
		}
		if err != nil {
			up.Error = err.Error()
			proto.Write(conn, &proto.Msg{Type: proto.Upgrade, Upgrade: &up})
			return fmt.Errorf("%w: cannot upgrade %s from %s to %s: %v", ErrUpgrade, h.Name, h.Version, e.o.Version, err)
		}
		up.Hash = fsx.Hash(bin)
		if err := proto.Write(conn, &proto.Msg{Type: proto.Upgrade, Upgrade: &up, Content: bin}); err != nil {
			return fmt.Errorf("%w: sending the upgrade to %s: %v", ErrUpgrade, h.Name, err)
		}
		// The older side closes once it has the whole binary. Closing first
		// could drop the frame's tail still in flight (ssh, the bridge).
		closed := make(chan struct{})
		go func() { io.Copy(io.Discard, conn); close(closed) }()
		select {
		case <-closed:
		case <-time.After(upgradeTimeout):
		}
		e.log.Info("upgrading peer", "peer", h.Name, "from", h.Version, "to", e.o.Version)
		return fmt.Errorf("%w: upgrading %s from %s to %s", ErrUpgrade, h.Name, h.Version, e.o.Version)
	}

	type res struct {
		m   *proto.Msg
		err error
	}
	got := make(chan res, 1)
	go func() {
		m, err := proto.Read(conn)
		got <- res{m, err}
	}()
	var r res
	select {
	case r = <-got:
	case <-time.After(upgradeTimeout):
		r.err = errors.New("timed out")
	}
	if r.err != nil || r.m.Type != proto.Upgrade || r.m.Upgrade == nil {
		return fmt.Errorf("%w: %s runs %s (this machine %s) but no upgrade arrived (%v)", ErrUpgrade, h.Name, h.Version, e.o.Version, r.err)
	}
	m := r.m
	if m.Upgrade.Error != "" {
		return fmt.Errorf("%w: %s runs %s but cannot upgrade this machine (%s): %s", ErrUpgrade, h.Name, h.Version, e.o.Version, m.Upgrade.Error)
	}
	if err := e.acceptUpgrade(*m.Upgrade, m.Content); err != nil {
		return fmt.Errorf("%w: upgrade from %s refused: %v", ErrUpgrade, h.Name, err)
	}
	return fmt.Errorf("%w: upgraded to %s from %s", ErrUpgrade, m.Upgrade.Version, h.Name)
}

// acceptUpgrade checks a received binary and installs it: the right
// platform, the hash it claims, and strictly newer than this version.
func (e *Engine) acceptUpgrade(up proto.UpgradeBody, bin []byte) error {
	if up.Platform != e.o.Platform {
		return fmt.Errorf("binary is for %s, this machine is %s", up.Platform, e.o.Platform)
	}
	if fsx.Hash(bin) != up.Hash {
		return errors.New("binary does not match its hash")
	}
	if c, ok := buildinfo.Compare(up.Version, e.o.Version); !ok || c <= 0 {
		return fmt.Errorf("%s is not newer than %s", up.Version, e.o.Version)
	}
	if e.o.InstallBinary == nil {
		return errors.New("this machine cannot replace its binary")
	}
	e.log.Info("upgrading this machine", "to", up.Version)
	return e.o.InstallBinary(up.Version, bin)
}
