package link

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSSHArgsMatchSpec(t *testing.T) {
	got := SSHArgs("devbox", "", "bridge")
	want := []string{"-T", "-o", "BatchMode=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "ControlMaster=no", "-o", "ControlPath=none", "devbox", "enigma bridge"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
	if got := SSHArgs("me@box", "~/bin/enigma", "identity"); got[len(got)-1] != "~/bin/enigma identity" {
		t.Fatalf("remote command = %q", got[len(got)-1])
	}
}

// fakeSSH writes a script standing in for ssh and points ENIGMA_SSH at it.
func fakeSSH(t *testing.T, body string) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	script := filepath.Join(dir, "ssh")
	os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argsFile+"\n"+body+"\n"), 0o755)
	t.Setenv("ENIGMA_SSH", script)
	return argsFile
}

func TestDialStreamsThroughSSH(t *testing.T) {
	argsFile := fakeSSH(t, "exec cat") // an echo "peer"
	conn, err := Dial(context.Background(), "devbox", "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo = %q, %v", buf, err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "devbox\nenigma bridge\n") {
		t.Fatalf("ssh args: %s", args)
	}
}

func TestDialSurfacesSSHErrors(t *testing.T) {
	fakeSSH(t, "echo 'devbox: Permission denied (publickey).' >&2; exit 255")
	conn, err := Dial(context.Background(), "devbox", "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = io.ReadAll(conn)
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("err = %v, want the ssh error text", err)
	}
}

func TestCloseKillsTheProcess(t *testing.T) {
	fakeSSH(t, "exec sleep 30")
	conn, err := Dial(context.Background(), "devbox", "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { conn.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close hung")
	}
}

func TestRunReturnsStdout(t *testing.T) {
	argsFile := fakeSSH(t, `echo '{"group_id":"g1"}'`)
	out, err := Run(context.Background(), "box", "/opt/enigma", "identity")
	if err != nil || strings.TrimSpace(string(out)) != `{"group_id":"g1"}` {
		t.Fatalf("out=%q err=%v", out, err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "box\n/opt/enigma identity\n") {
		t.Fatalf("ssh args: %s", args)
	}
}

func TestRunErrorIncludesStderr(t *testing.T) {
	fakeSSH(t, "echo 'sh: enigma: command not found' >&2; exit 127")
	_, err := Run(context.Background(), "box", "", "identity")
	if err == nil || !strings.Contains(err.Error(), "command not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestBridgeRelaysBothWays(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { // uppercase echo server standing in for the daemon
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b := make([]byte, 64)
		n, _ := c.Read(b)
		c.Write(bytes.ToUpper(b[:n]))
	}()
	inR, inW := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- Bridge(inR, &out, sock) }()
	inW.Write([]byte("hello"))
	time.Sleep(100 * time.Millisecond)
	inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not end")
	}
	if out.String() != "HELLO" {
		t.Fatalf("relayed %q", out.String())
	}
}

func TestBridgeWithoutDaemonExplains(t *testing.T) {
	err := Bridge(strings.NewReader(""), io.Discard, filepath.Join(t.TempDir(), "none.sock"))
	if err == nil || !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("err = %v", err)
	}
}
