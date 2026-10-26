package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/nadavushka-dev/enigma/internal/config"
	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/problems"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

// Client talks to the local daemon over its API socket.
type Client struct {
	sock string
	http *http.Client
}

func NewClient(p config.Paths) *Client {
	sock := p.APISocket()
	return &Client{sock: sock, http: &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}},
	}}
}

func (c *Client) Status() (StatusReport, error) {
	var r StatusReport
	return r, c.do(http.MethodGet, "/status", nil, &r)
}

// Problems returns the standing problems (SPEC §13.5), errors first.
func (c *Client) Problems() ([]problems.Problem, error) {
	var ps []problems.Problem
	return ps, c.do(http.MethodGet, "/problems", nil, &ps)
}

func (c *Client) Add(abs string, ignore []string) error {
	return c.do(http.MethodPost, "/add", pathRequest{Path: abs, Ignore: ignore}, nil)
}

func (c *Client) Remove(abs string) error {
	return c.do(http.MethodPost, "/remove", pathRequest{Path: abs}, nil)
}

// Reload makes the daemon re-read peers.json.
func (c *Client) Reload() error { return c.do(http.MethodPost, "/reload", nil, nil) }

// Events returns the daemon's events after seq, waiting up to wait (at
// most 20s) for one to happen.
func (c *Client) Events(after uint64, wait time.Duration) ([]engine.Event, error) {
	var evs []engine.Event
	return evs, c.do(http.MethodGet, fmt.Sprintf("/events?after=%d&wait=%d", after, int(wait.Seconds())), nil, &evs)
}

// Restart makes the daemon re-execute its binary.
func (c *Client) Restart() error { return c.do(http.MethodPost, "/restart", nil, nil) }

// Shutdown stops the daemon.
func (c *Client) Shutdown() error { return c.do(http.MethodPost, "/shutdown", nil, nil) }

// Limits returns the group's limits (SPEC §8).
func (c *Client) Limits() (engine.Limits, error) {
	var l engine.Limits
	return l, c.do(http.MethodGet, "/limits", nil, &l)
}

// SetLimits changes the group's limits; a zero field keeps its value. It
// returns the limits now in force.
func (c *Client) SetLimits(l engine.Limits) (engine.Limits, error) {
	var now engine.Limits
	return now, c.do(http.MethodPost, "/limits", l, &now)
}

// Machines returns every machine record in the group state.
func (c *Client) Machines() ([]proto.MachineRecord, error) {
	var rs []proto.MachineRecord
	return rs, c.do(http.MethodGet, "/machines", nil, &rs)
}

// SetMachine records r (add, rename, remove) in the group state.
func (c *Client) SetMachine(r proto.MachineRecord) error {
	return c.do(http.MethodPost, "/machine", r, nil)
}

func (c *Client) do(method, path string, body, out any) error {
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, "http://enigma"+path, rd)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) || isNoSocket(err) {
			return fmt.Errorf("the enigma daemon is not running (start it with `enigma daemon` or `enigma service install`)")
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e apiError
		json.NewDecoder(resp.Body).Decode(&e)
		return errors.New(e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func isNoSocket(err error) bool {
	var oe *net.OpError
	return errors.As(err, &oe)
}
