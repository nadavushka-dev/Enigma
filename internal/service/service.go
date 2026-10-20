// Package service runs the daemon in the background at login: a launchd
// agent on macOS, a systemd user unit with linger on Linux, or a plain
// background process where there is no service manager (SPEC §4, §11).
package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const label = "dev.enigma.daemon"

// Runner executes service-manager commands (launchctl, systemctl), and
// starts a detached process for the background fallback.
type Runner interface {
	Run(name string, args ...string) error
	Start(env []string, name string, args ...string) error
}

type execRunner struct{}

// Start runs name detached from this process: its own session, no stdio.
func (execRunner) Start(env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func (execRunner) Run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

type Options struct {
	GOOS          string
	Home          string
	XDGConfigHome string
	UID           int
	User          string            // for loginctl enable-linger
	Mode          string            // "" picks the service manager; "background" forces the fallback (ENIGMA_SERVICE)
	Bin           string            // absolute path of the enigma binary
	LogPath       string            // the daemon's own rotated log (--log)
	CrashLogPath  string            // the service manager's capture of stdout/stderr: crashes, panics
	Env           map[string]string // passed to the daemon (e.g. ENIGMA_HOME)
	Runner        Runner
}

// Defaults fills Options for the current machine and binary.
func Defaults(logPath, crashLogPath string) (Options, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Options{}, err
	}
	bin, err := os.Executable()
	if err != nil {
		return Options{}, err
	}
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return Options{}, err
	}
	env := map[string]string{}
	for _, k := range []string{"ENIGMA_HOME", "ENIGMA_SSH", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	return Options{GOOS: runtime.GOOS, Home: home, XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"), UID: os.Getuid(), User: name,
		Mode: os.Getenv("ENIGMA_SERVICE"), Bin: bin, LogPath: logPath, CrashLogPath: crashLogPath, Env: env, Runner: execRunner{}}, nil
}

// File is where the service definition lives.
func File(o Options) string {
	if o.GOOS == "darwin" {
		return filepath.Join(o.Home, "Library", "LaunchAgents", label+".plist")
	}
	cfg := o.XDGConfigHome
	if cfg == "" {
		cfg = filepath.Join(o.Home, ".config")
	}
	return filepath.Join(cfg, "systemd", "user", "enigma.service")
}

// Result says how the daemon now runs, and what the user should know.
type Result struct {
	Mode     string // launchd, systemd or background
	Warnings []string
}

func Install(o Options) (Result, error) {
	if o.Runner == nil {
		o.Runner = execRunner{}
	}
	if err := os.MkdirAll(filepath.Dir(o.LogPath), 0o700); err != nil {
		return Result{}, err
	}
	if o.Mode == "background" {
		return background(o)
	}
	p := File(o)
	switch o.GOOS {
	case "darwin":
		domain := "gui/" + strconv.Itoa(o.UID)
		_ = o.Runner.Run("launchctl", "bootout", domain, p) // not loaded yet is fine
		if err := writeFile(p, Plist(o)); err != nil {
			return Result{}, err
		}
		return Result{Mode: "launchd"}, o.Runner.Run("launchctl", "bootstrap", domain, p)
	case "linux":
		if o.Runner.Run("systemctl", "--user", "show-environment") != nil {
			return background(o) // containers, WSL, minimal servers
		}
		if err := writeFile(p, Unit(o)); err != nil {
			return Result{}, err
		}
		if err := o.Runner.Run("systemctl", "--user", "daemon-reload"); err != nil {
			return Result{}, err
		}
		if err := o.Runner.Run("systemctl", "--user", "enable", "--now", "enigma.service"); err != nil {
			return Result{}, err
		}
		res := Result{Mode: "systemd"}
		if err := o.Runner.Run("loginctl", "enable-linger", o.User); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("could not enable linger (%v): the daemon stops at logout until you run `loginctl enable-linger %s`", err, o.User))
		}
		return res, nil
	}
	return Result{}, fmt.Errorf("background service is not supported on %s; run `enigma daemon` under your own supervisor", o.GOOS)
}

// background starts the daemon as a detached process: no service manager,
// so it does not survive a reboot.
func background(o Options) (Result, error) {
	env := os.Environ()
	for _, k := range sortedKeys(o.Env) {
		env = append(env, k+"="+o.Env[k])
	}
	if err := o.Runner.Start(env, o.Bin, "daemon", "--log", o.LogPath); err != nil {
		return Result{}, err
	}
	return Result{Mode: "background", Warnings: []string{
		"no service manager here: the daemon runs in the background but will not survive a reboot; run `enigma service install` again after one"}}, nil
}

// Uninstall removes the service. A background daemon is not managed here;
// the caller stops it through its API.
func Uninstall(o Options) error {
	if o.Runner == nil {
		o.Runner = execRunner{}
	}
	if o.Mode == "background" {
		return nil
	}
	p := File(o)
	switch o.GOOS {
	case "darwin":
		_ = o.Runner.Run("launchctl", "bootout", "gui/"+strconv.Itoa(o.UID), p)
		return removeIfExists(p)
	case "linux":
		if o.Runner.Run("systemctl", "--user", "show-environment") != nil {
			return removeIfExists(p) // no systemd user session: nothing was installed with it
		}
		_ = o.Runner.Run("systemctl", "--user", "disable", "--now", "enigma.service")
		if err := removeIfExists(p); err != nil {
			return err
		}
		return o.Runner.Run("systemctl", "--user", "daemon-reload")
	}
	return fmt.Errorf("background service is not supported on %s", o.GOOS)
}

func Plist(o Options) string {
	var env strings.Builder
	if len(o.Env) > 0 {
		env.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, k := range sortedKeys(o.Env) {
			fmt.Fprintf(&env, "\t\t<key>%s</key><string>%s</string>\n", xmlEscape(k), xmlEscape(o.Env[k]))
		}
		env.WriteString("\t</dict>\n")
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>daemon</string><string>--log</string><string>%s</string></array>
%s	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ThrottleInterval</key><integer>10</integer>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, label, xmlEscape(o.Bin), xmlEscape(o.LogPath), env.String(), xmlEscape(o.CrashLogPath), xmlEscape(o.CrashLogPath))
}

func Unit(o Options) string {
	var env strings.Builder
	for _, k := range sortedKeys(o.Env) {
		fmt.Fprintf(&env, "Environment=%s\n", strconv.Quote(k+"="+o.Env[k]))
	}
	return fmt.Sprintf(`[Unit]
Description=Enigma file sync daemon
After=network-online.target

[Service]
ExecStart=%s daemon --log %s
%sRestart=always
RestartSec=5
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, strconv.Quote(o.Bin), strconv.Quote(o.LogPath), env.String(), o.CrashLogPath, o.CrashLogPath)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func writeFile(p, content string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func removeIfExists(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
