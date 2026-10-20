// Package daemon is the long-running Enigma process on one machine: it owns
// the engine, accepts peer links on the peer socket, dials configured peers
// with backoff, and serves the CLI over the API socket (SPEC §4, §5).
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
	"sort"
	"sync"
	"time"

	"github.com/nadavushka-dev/enigma/internal/config"
	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/link"
)

type Options struct {
	Paths config.Paths
	Home  string // the user's home; entries live under it
	Log   *slog.Logger

	// Dial opens a link to a peer; default: ssh to its target running
	// `enigma bridge` (link.Dial).
	Dial func(ctx context.Context, p config.Peer) (io.ReadWriteCloser, error)

	MinBackoff, MaxBackoff time.Duration // reconnect backoff, default 1s .. 60s
	Debounce               time.Duration // passed to the engine (0 = default)
	Ready                  func()        // called once sockets are up (tests)
}

// peerState is what the dial loop knows about one configured peer.
type peerState struct {
	cfg       config.Peer
	machineID string // learned at the first handshake
	lastErr   string
	cancel    context.CancelFunc
}

type daemon struct {
	o   Options
	id  config.Identity
	eng *engine.Engine
	log *slog.Logger

	mu    sync.Mutex
	peers map[string]*peerState // by target
	ctx   context.Context
	wg    sync.WaitGroup
}

// Run serves until ctx is cancelled.
func Run(ctx context.Context, o Options) error {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Dial == nil {
		o.Dial = func(ctx context.Context, p config.Peer) (io.ReadWriteCloser, error) {
			return link.Dial(ctx, p.Target, p.RemoteCmd)
		}
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

	eng, err := engine.New(engine.Options{Home: o.Home, DataDir: o.Paths.Data, MachineID: id.MachineID,
		Name: id.Name, GroupID: id.GroupID, Debounce: o.Debounce, Log: o.Log})
	if err != nil {
		return err
	}
	defer eng.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d := &daemon{o: o, id: id, eng: eng, log: o.Log.With("machine", id.Name), peers: map[string]*peerState{}, ctx: ctx}

	go d.acceptPeers(peerLn)
	srv := &http.Server{Handler: d.api()}
	go srv.Serve(apiLn)
	defer srv.Close()

	if err := d.reload(); err != nil {
		d.log.Error("peers", "err", err)
	}
	d.log.Info("daemon started", "group", id.GroupID, "data", o.Paths.Data)
	if o.Ready != nil {
		o.Ready()
	}
	<-ctx.Done()
	d.mu.Lock()
	for _, p := range d.peers {
		p.cancel()
	}
	d.mu.Unlock()
	d.wg.Wait()
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
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return nil, err
	}
	return net.Listen("unix", path)
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
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

// reload syncs the dial loops with peers.json.
func (d *daemon) reload() error {
	cfg, err := config.LoadPeers(d.o.Paths)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	want := map[string]config.Peer{}
	for _, p := range cfg {
		want[p.Target] = p
	}
	for target, ps := range d.peers {
		if p, ok := want[target]; !ok || p != ps.cfg {
			ps.cancel()
			delete(d.peers, target)
		}
	}
	for target, p := range want {
		if _, ok := d.peers[target]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(d.ctx)
		ps := &peerState{cfg: p, cancel: cancel}
		d.peers[target] = ps
		d.wg.Add(1)
		go d.dialLoop(ctx, ps)
	}
	return nil
}

// dialLoop keeps a link to one peer, reconnecting with exponential backoff.
func (d *daemon) dialLoop(ctx context.Context, ps *peerState) {
	defer d.wg.Done()
	backoff := d.o.MinBackoff
	for {
		if id := d.peerID(ps); id == "" || !d.linked(id) {
			err := d.dialOnce(ctx, ps)
			d.mu.Lock()
			if err != nil {
				ps.lastErr = err.Error()
			} else {
				ps.lastErr = ""
			}
			d.mu.Unlock()
			if err != nil {
				d.log.Debug("dial failed", "peer", ps.cfg.Name, "err", err, "retry_in", backoff)
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

func (d *daemon) dialOnce(ctx context.Context, ps *peerState) error {
	conn, err := d.o.Dial(ctx, ps.cfg)
	if err != nil {
		return err
	}
	who, err := d.eng.ConnectPeer(conn, true)
	if who.ID != "" {
		d.mu.Lock()
		ps.machineID = who.ID
		d.mu.Unlock()
	}
	if errors.Is(err, engine.ErrDuplicateLink) {
		return nil // already linked from the other side
	}
	return err
}

func (d *daemon) peerID(ps *peerState) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return ps.machineID
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

// ---- API ----------------------------------------------------------------------

// PeerReport merges configured peers (dialed by us) and live links.
type PeerReport struct {
	Name      string `json:"name"`
	ID        string `json:"id,omitempty"`
	Target    string `json:"target,omitempty"` // empty: it dialed us
	Connected bool   `json:"connected"`
	LastError string `json:"last_error,omitempty"`
}

type StatusReport struct {
	Machine   string               `json:"machine"`
	MachineID string               `json:"machine_id"`
	Group     string               `json:"group"`
	Entries   []engine.EntryStatus `json:"entries"`
	Peers     []PeerReport         `json:"peers"`
	Files     int                  `json:"files"`
	Conflicts []string             `json:"conflicts,omitempty"`
}

func (d *daemon) status() StatusReport {
	st := d.eng.Status()
	live := map[string]engine.PeerStatus{}
	for _, p := range st.Peers {
		live[p.ID] = p
	}
	r := StatusReport{Machine: d.id.Name, MachineID: d.id.MachineID, Group: d.id.GroupID,
		Entries: st.Entries, Files: st.Files, Conflicts: st.Conflicts}
	d.mu.Lock()
	for _, ps := range d.peers {
		pr := PeerReport{Name: ps.cfg.Name, ID: ps.machineID, Target: ps.cfg.Target, LastError: ps.lastErr}
		if lp, ok := live[ps.machineID]; ok && ps.machineID != "" {
			pr.Connected, pr.LastError = true, ""
			delete(live, lp.ID)
		}
		r.Peers = append(r.Peers, pr)
	}
	d.mu.Unlock()
	for _, lp := range live {
		r.Peers = append(r.Peers, PeerReport{Name: lp.Name, ID: lp.ID, Connected: true})
	}
	sort.Slice(r.Peers, func(i, j int) bool { return r.Peers[i].Name < r.Peers[j].Name })
	return r
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
	return mux
}
