// Package service runs the daemon in the background at login: a launchd
// agent on macOS, a systemd user unit on Linux (SPEC §4).
package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const label = "dev.enigma.daemon"

// Runner executes service-manager commands (launchctl, systemctl).
type Runner interface {
	Run(name string, args ...string) error
}

type execRunner struct{}

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
	Bin           string            // absolute path of the enigma binary
	LogPath       string            // daemon stdout/stderr
	Env           map[string]string // passed to the daemon (e.g. ENIGMA_HOME)
	Runner        Runner
}

// Defaults fills Options for the current machine and binary.
func Defaults(logPath string) (Options, error) {
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
	return Options{GOOS: runtime.GOOS, Home: home, XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"), UID: os.Getuid(),
		Bin: bin, LogPath: logPath, Env: env, Runner: execRunner{}}, nil
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

func Install(o Options) error {
	if o.Runner == nil {
		o.Runner = execRunner{}
	}
	if err := os.MkdirAll(filepath.Dir(o.LogPath), 0o700); err != nil {
		return err
	}
	p := File(o)
	switch o.GOOS {
	case "darwin":
		domain := "gui/" + strconv.Itoa(o.UID)
		_ = o.Runner.Run("launchctl", "bootout", domain, p) // not loaded yet is fine
		if err := writeFile(p, Plist(o)); err != nil {
			return err
		}
		return o.Runner.Run("launchctl", "bootstrap", domain, p)
	case "linux":
		if err := writeFile(p, Unit(o)); err != nil {
			return err
		}
		if err := o.Runner.Run("systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		return o.Runner.Run("systemctl", "--user", "enable", "--now", "enigma.service")
	}
	return fmt.Errorf("background service is not supported on %s; run `enigma daemon` under your own supervisor", o.GOOS)
}

func Uninstall(o Options) error {
	if o.Runner == nil {
		o.Runner = execRunner{}
	}
	p := File(o)
	switch o.GOOS {
	case "darwin":
		_ = o.Runner.Run("launchctl", "bootout", "gui/"+strconv.Itoa(o.UID), p)
		return removeIfExists(p)
	case "linux":
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
	<array><string>%s</string><string>daemon</string></array>
%s	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ThrottleInterval</key><integer>10</integer>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, label, xmlEscape(o.Bin), env.String(), xmlEscape(o.LogPath), xmlEscape(o.LogPath))
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
ExecStart=%s daemon
%sRestart=always
RestartSec=5
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, strconv.Quote(o.Bin), env.String(), o.LogPath, o.LogPath)
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
