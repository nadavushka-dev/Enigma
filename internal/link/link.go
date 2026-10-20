// Package link connects machines over the system ssh (SPEC §5): dialing a
// peer's bridge, one-shot remote commands, and the bridge itself, which
// relays an incoming ssh session into the local daemon's peer socket.
package link

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// SSHArgs is the exact ssh argv for running `<remoteCmd> <sub...>` on target.
// The link gets its own connection, never a shared ControlMaster session.
func SSHArgs(target, remoteCmd string, sub ...string) []string {
	if remoteCmd == "" {
		remoteCmd = "enigma"
	}
	return []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		target, strings.Join(append([]string{remoteCmd}, sub...), " "),
	}
}

// sshBinary is the system ssh, or $ENIGMA_SSH (tests and e2e harnesses).
func sshBinary() string {
	if s := os.Getenv("ENIGMA_SSH"); s != "" {
		return s
	}
	return "ssh"
}

// Dial starts `ssh target <remoteCmd> bridge` and returns its stdio as a
// stream to the peer's daemon. When the stream ends, reads report ssh's
// stderr (auth failures, "command not found", ...).
func Dial(ctx context.Context, target, remoteCmd string) (io.ReadWriteCloser, error) {
	cmd := exec.CommandContext(ctx, sshBinary(), SSHArgs(target, remoteCmd, "bridge")...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &tail{max: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ssh: %w", err)
	}
	return &procConn{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

type procConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *tail

	waitOnce sync.Once
	waitErr  error
}

func (p *procConn) wait() error {
	p.waitOnce.Do(func() { p.waitErr = p.cmd.Wait() })
	return p.waitErr
}

func (p *procConn) Read(b []byte) (int, error) {
	n, err := p.stdout.Read(b)
	if errors.Is(err, io.EOF) {
		werr := p.wait() // also flushes stderr
		if msg := strings.TrimSpace(p.stderr.String()); msg != "" {
			return n, fmt.Errorf("ssh: %s", msg)
		}
		if werr != nil {
			return n, fmt.Errorf("ssh: %w", werr)
		}
	}
	return n, err
}

func (p *procConn) Write(b []byte) (int, error) { return p.stdin.Write(b) }

func (p *procConn) Close() error {
	p.stdin.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	p.wait()
	return nil
}

// Run executes `<remoteCmd> <sub...>` on target and returns its stdout.
func Run(ctx context.Context, target, remoteCmd string, sub ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, sshBinary(), SSHArgs(target, remoteCmd, sub...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return out.Bytes(), fmt.Errorf("ssh %s: %s", target, msg)
		}
		return out.Bytes(), fmt.Errorf("ssh %s: %w", target, err)
	}
	return out.Bytes(), nil
}

// Shell runs a shell command on target (one ssh session, with stdin) and
// returns its stdout; errors carry ssh's or the command's stderr.
func Shell(ctx context.Context, target, command string, stdin io.Reader) ([]byte, error) {
	args := SSHArgs(target, "")
	args[len(args)-1] = command // a shell command, not an enigma subcommand
	cmd := exec.CommandContext(ctx, sshBinary(), args...)
	var out, errb bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, &out, &errb
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return out.Bytes(), fmt.Errorf("ssh %s: %s", target, msg)
		}
		return out.Bytes(), fmt.Errorf("ssh %s: %w", target, err)
	}
	return out.Bytes(), nil
}

// Resolve captures how this machine reaches alias, from `ssh -G`, as
// ssh://user@hostname:port: a target other machines can use even without
// the alias (SPEC §5).
func Resolve(ctx context.Context, alias string) (string, error) {
	out, err := exec.CommandContext(ctx, sshBinary(), "-G", alias).Output()
	if err != nil {
		return "", fmt.Errorf("ssh -G %s: %w", alias, err)
	}
	var user, host, port string
	for _, line := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch k {
		case "user":
			user = v
		case "hostname":
			host = v
		case "port":
			port = v
		}
	}
	if user == "" || host == "" || port == "" {
		return "", fmt.Errorf("ssh -G %s: no user, hostname or port in its output", alias)
	}
	if strings.Contains(host, ":") { // IPv6
		host = "[" + host + "]"
	}
	return "ssh://" + user + "@" + host + ":" + port, nil
}

// Bridge relays in/out (an ssh session's stdio) to the daemon's peer socket
// until either side ends.
func Bridge(in io.Reader, out io.Writer, peerSocket string) error {
	c, err := net.Dial("unix", peerSocket)
	if err != nil {
		return fmt.Errorf("the enigma daemon is not running on this machine (%v)", err)
	}
	defer c.Close()
	go func() {
		io.Copy(c, in)
		if uc, ok := c.(*net.UnixConn); ok {
			uc.CloseWrite() // tell the daemon the link ended
		}
	}()
	_, err = io.Copy(out, c)
	return err
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(b)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}
