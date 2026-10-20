package sshconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHostsSkipsPatternsAndFollowsIncludes(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".ssh/config"), `
# comment
Host devbox  laptop
  HostName 10.0.0.2
Host *.internal !bastion
  User me
Host *
  ServerAliveInterval 30
host lower-case
Include config.d/*.conf
Include ~/.ssh/extra
Match host foo
  User x
Host "quoted"
Host=equals
`)
	write(t, filepath.Join(home, ".ssh/config.d/a.conf"), "Host from-include\n  HostName x\n")
	write(t, filepath.Join(home, ".ssh/extra"), "Host extra devbox\n")
	got, err := Hosts(home)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"devbox", "equals", "extra", "from-include", "laptop", "lower-case", "quoted"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestIncludeCyclesStop(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".ssh/config"), "Include config\nHost a\n")
	got, err := Hosts(home)
	if err != nil || !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("%v %v", got, err)
	}
}

func TestNoConfigIsNoHosts(t *testing.T) {
	got, err := Hosts(t.TempDir())
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestHas(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".ssh/config"), "Host devbox\n")
	if !Has(home, "devbox") || Has(home, "other") {
		t.Fatal("Has")
	}
}
