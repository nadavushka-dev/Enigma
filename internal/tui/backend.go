package tui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nadavushka-dev/enigma/internal/config"
	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/fleet"
	"github.com/nadavushka-dev/enigma/internal/fuzzy"
	"github.com/nadavushka-dev/enigma/internal/proto"
	"github.com/nadavushka-dev/enigma/internal/sshconfig"
)

// Backend is everything the TUI does to the world. The live one talks to
// the local daemon and runs fleet operations over ssh; tests use a fake.
type Backend interface {
	Home() string
	Status() (daemon.StatusReport, error)
	Machines() ([]proto.MachineRecord, error)
	Hosts() ([]string, error) // aliases in ~/.ssh/config
	Add(abs string) error     // start syncing a file or folder
	Remove(abs string) error  // stop syncing an entry
	AddMachine(ctx context.Context, alias string, out io.Writer) error
	RemoveMachine(ctx context.Context, name string, uninstall bool, out io.Writer) error
	Events(after uint64, wait time.Duration) ([]engine.Event, error)
	ReadFile(rel string) ([]byte, error)            // a file under ~
	HomeIndex() *fuzzy.Index                        // the home index (SPEC §12.4), built on first use
	SetLimits(engine.Limits) (engine.Limits, error) // change the fleet's limits (SPEC §8); zero fields keep their value
}

// Live is the real Backend.
type Live struct {
	Paths config.Paths
	home  string
	cl    *daemon.Client

	indexOnce sync.Once
	index     *fuzzy.Index
}

func NewLive(p config.Paths) (*Live, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Live{Paths: p, home: home, cl: daemon.NewClient(p)}, nil
}

func (l *Live) Home() string                                     { return l.home }
func (l *Live) Status() (daemon.StatusReport, error)             { return l.cl.Status() }
func (l *Live) Machines() ([]proto.MachineRecord, error)         { return fleet.Members(l.cl) }
func (l *Live) Hosts() ([]string, error)                         { return sshconfig.Hosts(l.home) }
func (l *Live) Add(abs string) error                             { return l.cl.Add(abs, nil, "") }
func (l *Live) Remove(abs string) error                          { return l.cl.Remove(abs) }
func (l *Live) SetLimits(x engine.Limits) (engine.Limits, error) { return l.cl.SetLimits(x) }

func (l *Live) AddMachine(ctx context.Context, alias string, out io.Writer) error {
	return fleet.Add(ctx, l.cl, l.Paths, alias, "", out)
}

func (l *Live) RemoveMachine(ctx context.Context, name string, uninstall bool, out io.Writer) error {
	return fleet.Remove(ctx, l.cl, name, uninstall, out)
}

func (l *Live) Events(after uint64, wait time.Duration) ([]engine.Event, error) {
	return l.cl.Events(after, wait)
}

func (l *Live) ReadFile(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(l.home, filepath.FromSlash(rel)))
}

func (l *Live) HomeIndex() *fuzzy.Index {
	l.indexOnce.Do(func() {
		l.index = fuzzy.Build(l.home, fuzzy.Options{Skip: []string{l.Paths.Data, l.Paths.Config}})
	})
	return l.index
}
