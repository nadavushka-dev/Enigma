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

func (c *Client) Add(abs string, ignore []string) error {
	return c.do(http.MethodPost, "/add", pathRequest{Path: abs, Ignore: ignore}, nil)
}

func (c *Client) Remove(abs string) error {
	return c.do(http.MethodPost, "/remove", pathRequest{Path: abs}, nil)
}

// Reload makes the daemon re-read peers.json.
func (c *Client) Reload() error { return c.do(http.MethodPost, "/reload", nil, nil) }

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
