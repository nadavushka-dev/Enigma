package service

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls   [][]string
	started [][]string
	fail    map[string]bool // "name arg0" -> fails
}

func (f *fakeRunner) Run(name string, args ...string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	if len(args) > 0 && f.fail[name+" "+args[0]+" "+args[len(args)-1]] {
		return errors.New(name + " failed")
	}
	return nil
}

func (f *fakeRunner) Start(env []string, name string, args ...string) error {
	f.started = append(f.started, append([]string{name}, args...))
	return nil
}

func opts(t *testing.T, goos string) (Options, *fakeRunner) {
	home := t.TempDir()
	r := &fakeRunner{}
	return Options{GOOS: goos, Home: home, UID: 501, User: "dev", Bin: "/opt/en & co/enigma",
		LogPath: filepath.Join(home, "data", "enigma.log"), CrashLogPath: filepath.Join(home, "data", "daemon.out"),
		Env: map[string]string{"ENIGMA_HOME": "/tmp/eh"}, Runner: r}, r
}

func TestPlistContent(t *testing.T) {
	o, _ := opts(t, "darwin")
	p := Plist(o)
	for _, want := range []string{
		"<string>dev.enigma.daemon</string>",
		"<string>/opt/en &amp; co/enigma</string><string>daemon</string><string>--log</string><string>" + o.LogPath + "</string>", // XML-escaped
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key><true/>",
		"<key>ENIGMA_HOME</key><string>/tmp/eh</string>",
		// The daemon rotates its own log; launchd only catches crashes.
		"<key>StandardErrorPath</key><string>" + o.CrashLogPath + "</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
}

func TestUnitContent(t *testing.T) {
	o, _ := opts(t, "linux")
	u := Unit(o)
	for _, want := range []string{
		"ExecStart=\"/opt/en & co/enigma\" daemon --log \"" + o.LogPath + "\"",
		"StandardError=append:" + o.CrashLogPath,
		"Restart=always",
		"Environment=\"ENIGMA_HOME=/tmp/eh\"",
		"WantedBy=default.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
}

func TestInstallLaunchd(t *testing.T) {
	o, r := opts(t, "darwin")
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(o.Home, "Library/LaunchAgents/dev.enigma.daemon.plist")
	if b, err := os.ReadFile(plist); err != nil || string(b) != Plist(o) {
		t.Fatalf("plist not written: %v", err)
	}
	want := [][]string{
		{"launchctl", "bootout", "gui/501", plist}, // reinstall-safe
		{"launchctl", "bootstrap", "gui/501", plist},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %q", r.calls)
	}
	if _, err := os.Stat(filepath.Dir(o.LogPath)); err != nil {
		t.Fatal("log directory must exist before launchd opens the log")
	}
}

func TestInstallSystemd(t *testing.T) {
	o, r := opts(t, "linux")
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(o.Home, ".config/systemd/user/enigma.service")
	if b, err := os.ReadFile(unit); err != nil || string(b) != Unit(o) {
		t.Fatalf("unit not written: %v", err)
	}
	want := [][]string{
		{"systemctl", "--user", "show-environment"}, // is there a user session?
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", "enigma.service"},
		{"loginctl", "enable-linger", "dev"}, // keep running after logout
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %q", r.calls)
	}
}

func TestSystemdRespectsXDGConfigHome(t *testing.T) {
	o, _ := opts(t, "linux")
	o.XDGConfigHome = filepath.Join(o.Home, "xdg")
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(o.XDGConfigHome, "systemd/user/enigma.service")); err != nil {
		t.Fatal(err)
	}
}

func TestUninstall(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		o, r := opts(t, goos)
		Install(o)
		r.calls = nil
		if err := Uninstall(o); err != nil {
			t.Fatal(err)
		}
		if p := File(o); fileExists(p) {
			t.Errorf("%s: %s still exists", goos, p)
		}
		if len(r.calls) == 0 {
			t.Errorf("%s: service manager not told", goos)
		}
		if err := Uninstall(o); err != nil {
			t.Errorf("%s: uninstalling twice must be fine: %v", goos, err)
		}
	}
}

func TestUnsupportedOS(t *testing.T) {
	o, _ := opts(t, "windows")
	if _, err := Install(o); err == nil || !strings.Contains(err.Error(), "windows") {
		t.Fatalf("err = %v", err)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestPlistPassesPlutil(t *testing.T) {
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil not available (not macOS)")
	}
	o, _ := opts(t, "darwin")
	p := filepath.Join(t.TempDir(), "x.plist")
	os.WriteFile(p, []byte(Plist(o)), 0o644)
	if out, err := exec.Command(plutil, "-lint", p).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v\n%s", err, out)
	}
}

func TestLingerFailureIsAWarning(t *testing.T) {
	o, r := opts(t, "linux")
	r.fail = map[string]bool{"loginctl enable-linger dev": true}
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "systemd" || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "logout") {
		t.Fatalf("result %+v", res)
	}
}

func TestNoSystemdUserSessionFallsBackToBackground(t *testing.T) {
	o, r := opts(t, "linux")
	r.fail = map[string]bool{"systemctl --user show-environment": true}
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "background" || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "reboot") {
		t.Fatalf("result %+v", res)
	}
	want := [][]string{{o.Bin, "daemon", "--log", o.LogPath}}
	if !reflect.DeepEqual(r.started, want) {
		t.Fatalf("started %q", r.started)
	}
	if fileExists(File(o)) {
		t.Fatal("unit written without systemd")
	}
}

func TestForcedBackgroundNeverTouchesTheServiceManager(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		o, r := opts(t, goos)
		o.Mode = "background"
		res, err := Install(o)
		if err != nil || res.Mode != "background" {
			t.Fatalf("%s: %+v %v", goos, res, err)
		}
		if len(r.calls) != 0 || len(r.started) != 1 {
			t.Fatalf("%s: calls %q started %q", goos, r.calls, r.started)
		}
		r.calls = nil
		if err := Uninstall(o); err != nil || len(r.calls) != 0 {
			t.Fatalf("%s: uninstall %v calls %q", goos, err, r.calls)
		}
	}
}
