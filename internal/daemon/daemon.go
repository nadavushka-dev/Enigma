// Package daemon is the long-running Enigma process on one machine: it owns
// the engine, accepts peer links on the peer socket, dials every member with
// backoff, and serves the CLI over the API socket (SPEC §4, §5, §11).
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nadavushka-dev/enigma/internal/buildinfo"
	"github.com/nadavushka-dev/enigma/internal/config"
	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/link"
	"github.com/nadavushka-dev/enigma/internal/problems"
	"github.com/nadavushka-dev/enigma/internal/proto"
	"github.com/nadavushka-dev/enigma/internal/release"
	"github.com/nadavushka-dev/enigma/internal/sshconfig"
)

type Options struct {
	Paths config.Paths
	Home  string // the user's home; entries live under it
	Log   *slog.Logger

	// Dial opens a link to a machine; default: ssh to target running
	// `<remoteCmd> bridge` (link.Dial).
	Dial func(ctx context.Context, target, remoteCmd string) (io.ReadWriteCloser, error)
	// HasAlias reports whether this machine's ssh config has an alias;
	// default: sshconfig.Has(Home, alias).
	HasAlias func(alias string) bool

	Bin     string // this binary; default: os.Executable
	Version string // default: buildinfo.Version

	MinBackoff, MaxBackoff time.Duration // reconnect backoff, default 1s .. 60s
	Debounce               time.Duration // passed to the engine (0 = default)
	Ready                  func()        // called once sockets are up (tests)
	// Notify raises desktop notifications (SPEC §12.3), subject to
	// config.toml `notifications`; nil (tests) is silent.
	Notify func(path, title, body string)
}

// ErrRestart: the binary was replaced by an upgrade; the caller re-executes
// it (SPEC §11).
var ErrRestart = errors.New("restart into the upgraded binary")

// peerState is what the dial loop knows about one member.
type peerState struct {
	rec     proto.MachineRecord
	targets []string // in the order of SPEC §5
	good    int      // index of the target that last connected
	lastErr string
	cancel  context.CancelFunc
}

type daemon struct {
	o   Options
	id  config.Identity
	eng *engine.Engine
	log *slog.Logger

	mu      sync.Mutex
	peers   map[string]*peerState // by machine ID
	tracker problems.Tracker      // standing problems (SPEC §13.5); under mu
	ctx     context.Context
	stop    context.CancelFunc
	restart bool
	wg      sync.WaitGroup
}

// Run serves until ctx is cancelled, or returns ErrRestart after an upgrade.
func Run(ctx context.Context, o Options) error {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Dial == nil {
		o.Dial = link.Dial
	}
	if o.HasAlias == nil {
		o.HasAlias = func(a string) bool { return sshconfig.Has(o.Home, a) }
	}
	if o.Version == "" {
		o.Version = buildinfo.Version
	}
	if o.Bin == "" {
		bin, err := os.Executable()
		if err == nil {
			bin, err = filepath.EvalSymlinks(bin)
		}
		if err != nil {
			return err
		}
		o.Bin = bin
	}
	if o.MinBackoff == 0 {
		o.MinBackoff = time.Second
	}
	if o.MaxBackoff == 0 {
		o.MaxBackoff = time.Minute
	}
	id, err := config.LoadIdentity(o.Paths)
	if err != nil {
		return err
	}
	bootstrap, err := config.LoadBootstrap(o.Paths)
	if err != nil {
		return err
	}
	apiLn, err := listen(o.Paths.APISocket())
	if err != nil {
		return err
	}
	defer apiLn.Close()
	peerLn, err := listen(o.Paths.PeerSocket())
	if err != nil {
		return err
	}
	defer peerLn.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d := &daemon{o: o, id: id, log: o.Log.With("machine", id.Name), peers: map[string]*peerState{}, ctx: ctx, stop: cancel}
	recheck := make(chan struct{}, 1)
	notify := o.Notify
	if local, err := config.LoadLocal(o.Paths); err == nil && !local.Notifications {
		notify = nil
	}
	eng, err := engine.New(engine.Options{Home: o.Home, DataDir: o.Paths.Data, MachineID: id.MachineID,
		Name: id.Name, GroupID: id.GroupID, Debounce: o.Debounce, Log: o.Log, Notify: notify,
		Bin: o.Bin, Bootstrap: bootstrap, Version: o.Version,
		OnMachines: func([]proto.MachineRecord) {
			select {
			case recheck <- struct{}{}:
			default:
			}
		},
		BinaryFor: d.binaryFor, InstallBinary: d.installBinary})
	if err != nil {
		return err
	}
	defer eng.Close()
	config.ClearBootstrap(o.Paths) // the engine holds them now
	d.eng = eng

	go d.acceptPeers(peerLn)
	srv := &http.Server{Handler: d.api()}
	go srv.Serve(apiLn)
	defer srv.Close()

	d.reload()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-recheck:
				d.reload()
			}
		}
	}()
	d.log.Info("daemon started", "group", id.GroupID, "version", o.Version, "data", o.Paths.Data)
	if o.Ready != nil {
		o.Ready()
	}
	<-ctx.Done()
	d.mu.Lock()
	for _, p := range d.peers {
		p.cancel()
	}
	restart := d.restart
	d.mu.Unlock()
	d.wg.Wait()
	if restart {
		d.log.Info("daemon restarting into the upgraded binary")
		return ErrRestart
	}
	d.log.Info("daemon stopped")
	return nil
}

// listen refuses to start next to a live daemon and clears stale sockets.
func listen(path string) (net.Listener, error) {
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		return nil, fmt.Errorf("an enigma daemon is already running (%s)", path)
	}
	os.Remove(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return net.Listen("unix", path)
}

func (d *daemon) acceptPeers(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			if _, err := d.eng.ConnectPeer(c, false); err != nil && !errors.Is(err, engine.ErrDuplicateLink) {
				d.log.Warn("incoming link refused", "err", err)
			}
		}()
	}
}

// Targets lists how to reach r, in order (SPEC §5): a local override, its
// alias if this machine's ssh config has it, its captured target.
func Targets(r proto.MachineRecord, overrides map[string]string, hasAlias func(string) bool) []string {
	var out []string
	add := func(t string) {
		if t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	add(overrides[r.Name])
	if r.Alias != "" && hasAlias(r.Alias) {
		add(r.Alias)
	}
	add(r.Target)
	return out
}

// reload syncs the dial loops with the machine records and local overrides.
func (d *daemon) reload() error {
	local, err := config.LoadLocal(d.o.Paths)
	if err != nil {
		d.log.Error("local settings", "err", err)
	}
	want := map[string]*peerState{}
	for _, r := range d.eng.Machines() {
		if r.ID == d.id.MachineID || r.Removed {
			continue
		}
		want[r.ID] = &peerState{rec: r, targets: Targets(r, local.Aliases, d.o.HasAlias)}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, ps := range d.peers {
		if w, ok := want[id]; !ok || !slices.Equal(w.targets, ps.targets) || w.rec.Bin != ps.rec.Bin {
			ps.cancel()
			delete(d.peers, id)
		} else {
			ps.rec = w.rec // a rename needs no redial
		}
	}
	for id, ps := range want {
		if _, ok := d.peers[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(d.ctx)
		ps.cancel = cancel
		d.peers[id] = ps
		if len(ps.targets) > 0 { // otherwise it dials us (§11)
			d.wg.Add(1)
			go d.dialLoop(ctx, id, ps)
		}
	}
	return err
}

// dialLoop keeps a link to one member, reconnecting with exponential backoff.
func (d *daemon) dialLoop(ctx context.Context, id string, ps *peerState) {
	defer d.wg.Done()
	backoff := d.o.MinBackoff
	for {
		if !d.linked(id) {
			err := d.dialOnce(ctx, ps)
			if ctx.Err() != nil {
				return
			}
			d.mu.Lock()
			name := ps.rec.Name
			if err != nil {
				ps.lastErr = err.Error()
			} else {
				ps.lastErr = ""
			}
			d.mu.Unlock()
			if err != nil {
				d.log.Debug("dial failed", "peer", name, "err", err, "retry_in", backoff)
				if !sleep(ctx, backoff) {
					return
				}
				backoff = min(backoff*2, d.o.MaxBackoff)
				continue
			}
			backoff = d.o.MinBackoff
		}
		if !sleep(ctx, d.o.MinBackoff) { // check the link periodically
			return
		}
	}
}

// dialOnce tries each target, starting with the last one that worked.
func (d *daemon) dialOnce(ctx context.Context, ps *peerState) error {
	d.mu.Lock()
	targets, good, bin := ps.targets, ps.good, ps.rec.Bin
	d.mu.Unlock()
	var lastErr error
	for i := range targets {
		idx := (good + i) % len(targets)
		conn, err := d.o.Dial(ctx, targets[idx], bin)
		if err != nil {
			lastErr = err
			continue
		}
		_, err = d.eng.ConnectPeer(conn, true)
		if err == nil || errors.Is(err, engine.ErrDuplicateLink) {
			d.mu.Lock()
			ps.good = idx
			d.mu.Unlock()
			return nil
		}
		if errors.Is(err, engine.ErrUpgrade) {
			d.mu.Lock()
			ps.good = idx
			d.mu.Unlock()
			return err // reached it; the versions differ
		}
		lastErr = err
	}
	return lastErr
}

func (d *daemon) linked(machineID string) bool {
	for _, p := range d.eng.Status().Peers {
		if p.ID == machineID {
			return true
		}
	}
	return false
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ---- one version everywhere (SPEC §11) ------------------------------------------

// binaryFor is this version's binary for platform: this one, the cache,
// or the release download (SPEC §11).
func (d *daemon) binaryFor(platform string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(d.ctx, 2*time.Minute)
	defer cancel()
	b, _, err := release.BinaryFor(ctx, release.Default(), d.o.Paths.Data, d.o.Bin, d.o.Version, buildinfo.Platform(), platform)
	return b, err
}

// installBinary replaces this binary with bin, once bin itself says it is
// version, and has the daemon restart into it.
func (d *daemon) installBinary(version string, bin []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(d.o.Bin), ".enigma-upgrade-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	// Generous: the first run of a new binary can be slow (macOS checks it).
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, tmp.Name(), "version").Output()
	if f := strings.Fields(string(out)); err != nil || len(f) < 2 || f[1] != version {
		return fmt.Errorf("the received binary is not %s (it says %q)", version, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmp.Name(), d.o.Bin); err != nil {
		return err
	}
	d.mu.Lock()
	d.restart = true
	d.mu.Unlock()
	d.stop()
	return nil
}

// ---- API ----------------------------------------------------------------------

// PeerReport is one member as this machine sees it.
type PeerReport struct {
	Name      string `json:"name"`
	ID        string `json:"id,omitempty"`
	Target    string `json:"target,omitempty"` // what this machine dials; empty: it dials us
	Connected bool   `json:"connected"`
	LastError string `json:"last_error,omitempty"`
}

type StatusReport struct {
	Machine   string               `json:"machine"`
	MachineID string               `json:"machine_id"`
	Group     string               `json:"group"`
	Version   string               `json:"version"`
	Entries   []engine.EntryStatus `json:"entries"`
	Peers     []PeerReport         `json:"peers"`
	Files     int                  `json:"files"`
	Conflicts []string             `json:"conflicts,omitempty"`
	Limits    engine.Limits        `json:"limits"`
	Problems  []problems.Problem   `json:"problems"` // standing problems (SPEC §13.5), errors first
}

func (d *daemon) status() StatusReport {
	st := d.eng.Status()
	live := map[string]engine.PeerStatus{}
	for _, p := range st.Peers {
		live[p.ID] = p
	}
	r := StatusReport{Machine: d.id.Name, MachineID: d.id.MachineID, Group: d.id.GroupID, Version: d.o.Version,
		Entries: st.Entries, Files: st.Files, Conflicts: st.Conflicts, Limits: st.Limits}
	d.mu.Lock()
	for id, ps := range d.peers {
		pr := PeerReport{Name: ps.rec.Name, ID: id, LastError: ps.lastErr}
		if len(ps.targets) > 0 {
			pr.Target = ps.targets[ps.good]
		}
		if _, ok := live[id]; ok {
			pr.Connected, pr.LastError = true, ""
			delete(live, id)
		}
		r.Peers = append(r.Peers, pr)
	}
	d.mu.Unlock()
	for _, lp := range live { // linked before its record arrived
		r.Peers = append(r.Peers, PeerReport{Name: lp.Name, ID: lp.ID, Connected: true})
	}
	sort.Slice(r.Peers, func(i, j int) bool { return r.Peers[i].Name < r.Peers[j].Name })
	r.Problems = d.problems(r)
	return r
}

// problems derives the standing problems from a status report (SPEC
// §13.5), keeping each problem's Since while it stands.
func (d *daemon) problems(r StatusReport) []problems.Problem {
	in := problems.Input{Entries: r.Entries, Limits: r.Limits, Events: d.eng.Events(0)}
	for _, p := range r.Peers {
		in.Peers = append(in.Peers, problems.Peer{Name: p.Name, Dials: p.Target != "", Connected: p.Connected, LastError: p.LastError})
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	ps := d.tracker.Update(in, time.Now())
	if ps == nil {
		ps = []problems.Problem{}
	}
	return ps
}

type pathRequest struct {
	Path   string   `json:"path"`
	Ignore []string `json:"ignore,omitempty"`
}

type apiError struct {
	Error string `json:"error"`
}

func (d *daemon) api() http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any, err error) {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(apiError{Error: err.Error()})
			return
		}
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		reply(w, d.status(), nil)
	})
	mux.HandleFunc("GET /problems", func(w http.ResponseWriter, r *http.Request) {
		reply(w, d.status().Problems, nil)
	})
	mux.HandleFunc("POST /add", func(w http.ResponseWriter, r *http.Request) {
		var req pathRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			reply(w, nil, err)
			return
		}
		reply(w, struct{}{}, d.eng.Add(req.Path, req.Ignore))
	})
	mux.HandleFunc("POST /remove", func(w http.ResponseWriter, r *http.Request) {
		var req pathRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			reply(w, nil, err)
			return
		}
		reply(w, struct{}{}, d.eng.Remove(req.Path))
	})
	mux.HandleFunc("POST /reload", func(w http.ResponseWriter, r *http.Request) {
		reply(w, struct{}{}, d.reload())
	})
	mux.HandleFunc("POST /restart", func(w http.ResponseWriter, r *http.Request) {
		// Re-execute this binary (after `enigma update` replaced it).
		reply(w, struct{}{}, nil)
		d.mu.Lock()
		d.restart = true
		d.mu.Unlock()
		go d.stop()
	})
	mux.HandleFunc("POST /shutdown", func(w http.ResponseWriter, r *http.Request) {
		reply(w, struct{}{}, nil)
		go d.stop()
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		// Long poll: wait up to `wait` seconds for events after `after`.
		after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
		wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
		deadline := time.Now().Add(time.Duration(min(max(wait, 0), 20)) * time.Second)
		for {
			evs := d.eng.Events(after)
			if len(evs) > 0 || !time.Now().Before(deadline) {
				if evs == nil {
					evs = []engine.Event{}
				}
				reply(w, evs, nil)
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-d.ctx.Done():
				reply(w, []engine.Event{}, nil)
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	})
	mux.HandleFunc("GET /limits", func(w http.ResponseWriter, r *http.Request) {
		reply(w, d.eng.Limits(), nil)
	})
	mux.HandleFunc("POST /limits", func(w http.ResponseWriter, r *http.Request) {
		var l engine.Limits
		if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
			reply(w, nil, err)
			return
		}
		if err := d.eng.SetLimits(l); err != nil {
			reply(w, nil, err)
			return
		}
		reply(w, d.eng.Limits(), nil)
	})
	mux.HandleFunc("GET /machines", func(w http.ResponseWriter, r *http.Request) {
		reply(w, d.eng.Machines(), nil)
	})
	mux.HandleFunc("POST /machine", func(w http.ResponseWriter, r *http.Request) {
		var rec proto.MachineRecord
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			reply(w, nil, err)
			return
		}
		reply(w, struct{}{}, d.eng.SetMachine(rec))
	})
	return mux
}
