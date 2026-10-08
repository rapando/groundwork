package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"
)

// LogPath is where a background service writes its output.
func LogPath(home string) string { return filepath.Join(home, "groundwork.log") }

// StartBackground launches `groundwork serve` detached from this terminal and
// waits until it answers.
func StartBackground(home string, port int) (ServerInfo, error) {
	if si, ok := Running(home); ok {
		return si, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return ServerInfo{}, err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return ServerInfo{}, err
	}
	logf, err := os.OpenFile(LogPath(home), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return ServerInfo{}, err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "serve", "--no-open", "--port", strconv.Itoa(port))
	cmd.Env = append(os.Environ(), "GROUNDWORK_HOME="+home)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // survives the terminal closing
	if err := cmd.Start(); err != nil {
		return ServerInfo{}, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.After(20 * time.Second)
	for {
		if si, ok := Running(home); ok {
			return si, nil
		}
		select {
		case err := <-exited:
			return ServerInfo{}, fmt.Errorf("groundwork service exited (%v); see %s", err, LogPath(home))
		case <-deadline:
			return ServerInfo{}, fmt.Errorf("groundwork service did not start; see %s", LogPath(home))
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// Stop asks a running service to shut down (SIGTERM: runs stop gracefully).
// Under launchd/systemd the unit is stopped instead, so it isn't restarted.
func Stop(home string) error {
	if m := manager(); m != nil && m.installed() {
		return m.stop()
	}
	si, ok := Running(home)
	if !ok {
		return errors.New("groundwork service is not running")
	}
	p, err := os.FindProcess(si.PID)
	if err != nil {
		return err
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	for range 120 { // runs get up to 25s to release state locks
		if _, ok := Running(home); !ok {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.New("groundwork service is still stopping")
}

// ---- login service (launchd / systemd --user) ----

type svcManager interface {
	path() string
	installed() bool
	install(unit string) error
	uninstall() error
	start() error
	stop() error
	render(exe, home, path string) (string, error)
}

func manager() svcManager {
	switch runtime.GOOS {
	case "darwin":
		return launchd{}
	case "linux":
		return systemd{}
	}
	return nil
}

// Install registers groundwork to start at login and starts it now. It
// records the current PATH, since login services otherwise get a minimal one
// and wouldn't find terraform, ansible or the linters.
func Install(home string) (string, error) {
	m := manager()
	if m == nil {
		return "", fmt.Errorf("installing a login service is not supported on %s; run `groundwork serve` instead", runtime.GOOS)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	if si, ok := Running(home); ok && !m.installed() {
		// a background instance holds the port; hand over to the login service
		if p, err := os.FindProcess(si.PID); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
		for i := 0; i < 120; i++ {
			if _, ok := Running(home); !ok {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	unit, err := m.render(exe, home, os.Getenv("PATH"))
	if err != nil {
		return "", err
	}
	if err := m.install(unit); err != nil {
		return "", err
	}
	return m.path(), nil
}

func Uninstall() (string, error) {
	m := manager()
	if m == nil || !m.installed() {
		return "", errors.New("groundwork is not installed as a login service")
	}
	return m.path(), m.uninstall()
}

// Installed reports whether a login service unit exists.
func Installed() bool {
	m := manager()
	return m != nil && m.installed()
}

// StartInstalled starts the login service if one is installed.
func StartInstalled() (bool, error) {
	m := manager()
	if m == nil || !m.installed() {
		return false, nil
	}
	return true, m.start()
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func renderTemplate(t string, data any) (string, error) {
	var b strings.Builder
	err := template.Must(template.New("unit").Parse(t)).Execute(&b, data)
	return b.String(), err
}

type unitData struct{ Exe, Home, Path, Log string }

// launchd

const launchdLabel = "io.github.rapando.groundwork"

type launchd struct{}

func (launchd) path() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "LaunchAgents", launchdLabel+".plist")
}

func (l launchd) installed() bool { _, err := os.Stat(l.path()); return err == nil }

func (launchd) domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func (l launchd) render(exe, home, path string) (string, error) {
	return renderTemplate(launchdPlist, unitData{Exe: xmlEscape(exe), Home: xmlEscape(home), Path: xmlEscape(path), Log: xmlEscape(LogPath(home))})
}

func (l launchd) install(unit string) error {
	if l.installed() {
		_ = run("launchctl", "bootout", l.domain()+"/"+launchdLabel)
	}
	if err := os.MkdirAll(filepath.Dir(l.path()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(l.path(), []byte(unit), 0o644); err != nil {
		return err
	}
	return run("launchctl", "bootstrap", l.domain(), l.path())
}

func (l launchd) uninstall() error {
	_ = run("launchctl", "bootout", l.domain()+"/"+launchdLabel)
	return os.Remove(l.path())
}

func (l launchd) start() error {
	if err := run("launchctl", "bootstrap", l.domain(), l.path()); err != nil && !strings.Contains(err.Error(), "already") {
		return run("launchctl", "kickstart", l.domain()+"/"+launchdLabel)
	}
	return nil
}

// stop unloads the job: with KeepAlive, a plain kill would be restarted.
// It loads again at next login, or with `groundwork service start`.
func (l launchd) stop() error { return run("launchctl", "bootout", l.domain()+"/"+launchdLabel) }

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

const launchdPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + launchdLabel + `</string>
  <key>ProgramArguments</key>
  <array><string>{{.Exe}}</string><string>serve</string><string>--no-open</string></array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>GROUNDWORK_HOME</key><string>{{.Home}}</string>
    <key>PATH</key><string>{{.Path}}</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ExitTimeOut</key><integer>40</integer>
  <key>StandardOutPath</key><string>{{.Log}}</string>
  <key>StandardErrorPath</key><string>{{.Log}}</string>
</dict>
</plist>
`

// systemd --user

type systemd struct{}

func (systemd) path() string {
	d, err := os.UserConfigDir()
	if err != nil {
		h, _ := os.UserHomeDir()
		d = filepath.Join(h, ".config")
	}
	return filepath.Join(d, "systemd", "user", "groundwork.service")
}

func (s systemd) installed() bool { _, err := os.Stat(s.path()); return err == nil }

func (systemd) render(exe, home, path string) (string, error) {
	q := func(v string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(v) + `"` }
	return renderTemplate(systemdUnit, unitData{Exe: q(exe), Home: q("GROUNDWORK_HOME=" + home), Path: q("PATH=" + path)})
}

func (s systemd) install(unit string) error {
	if err := os.MkdirAll(filepath.Dir(s.path()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.path(), []byte(unit), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	return run("systemctl", "--user", "enable", "--now", "groundwork.service")
}

func (s systemd) uninstall() error {
	_ = run("systemctl", "--user", "disable", "--now", "groundwork.service")
	if err := os.Remove(s.path()); err != nil {
		return err
	}
	return run("systemctl", "--user", "daemon-reload")
}

func (systemd) start() error { return run("systemctl", "--user", "start", "groundwork.service") }
func (systemd) stop() error  { return run("systemctl", "--user", "stop", "groundwork.service") }

const systemdUnit = `[Unit]
Description=groundwork: local console for Terraform and Ansible

[Service]
ExecStart={{.Exe}} serve --no-open
Environment={{.Home}}
Environment={{.Path}}
Restart=on-failure
KillSignal=SIGTERM
TimeoutStopSec=40

[Install]
WantedBy=default.target
`
