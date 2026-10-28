package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/browser"

	"github.com/rapando/groundwork/v2/internal/auth"
	"github.com/rapando/groundwork/v2/internal/server"
)

type Options struct {
	Home        string // default Home()
	Host        string
	Port        int
	NoOpen      bool
	AllowRemote bool
	Version     string
	Commit      string
	// Log overrides the service logger (tests). When nil, Run logs to
	// <home>/groundwork.log through a size-capped, rotating writer, at
	// LogLevel in LogFormat, and also to stderr when LogToStderr is set.
	Log         *slog.Logger
	LogLevel    string // debug | info | warn | error (default info)
	LogFormat   string // text | json (default text)
	LogToStderr bool
	Add         []string  // folders to import at startup; the browser opens the first
	Out         io.Writer // startup messages; default os.Stdout
}

// ServerInfo is persisted to <home>/server.json so other invocations (the
// CLI, a second `serve`) can find the running service. The CLI's token is not
// part of the file: Running reads it from <home>/token.
type ServerInfo struct {
	PID     int    `json:"pid"`
	Port    int    `json:"port"`
	Version string `json:"version,omitempty"` // the binary serving; the CLI warns when it differs from its own
	Token   string `json:"-"`
}

func (si ServerInfo) Base() string { return "http://127.0.0.1:" + strconv.Itoa(si.Port) }

// URL is the console's address for path. It carries no credentials: the
// browser signs in with a password or passkey. It names localhost, the one
// origin passkeys and the session cookie are bound to.
func (si ServerInfo) URL(path string) string { return URLFor("localhost", si, path) }

func URLFor(host string, si ServerInfo, path string) string {
	if path == "" {
		path = "/"
	}
	return fmt.Sprintf("http://%s%s", net.JoinHostPort(host, strconv.Itoa(si.Port)), path)
}

// browserHost is the name a URL for the bound host uses.
func browserHost(host string) string {
	if isLoopback(host) {
		return "localhost"
	}
	return host
}

// ProjectPath is where a project's UI lives.
func ProjectPath(id string) string { return "/p/" + id + "/" }

// Run starts the service and blocks until interrupted. If a service is
// already running for this home, the folders in o.Add are imported into it.
func Run(ctx context.Context, o Options) error {
	if o.Host == "" {
		o.Host = "127.0.0.1"
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if !isLoopback(o.Host) && !o.AllowRemote {
		return fmt.Errorf("refusing to bind %s: pass --i-understand-remote-access to expose groundwork beyond this machine", o.Host)
	}
	home := o.Home
	if home == "" {
		h, err := Home()
		if err != nil {
			return err
		}
		home = h
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	if o.Log == nil {
		lw, err := openRotatingLog(LogPath(home), logMaxBytes, logKeep)
		if err != nil {
			return err
		}
		defer lw.Close()
		var w io.Writer = lw
		if o.LogToStderr {
			w = io.MultiWriter(lw, os.Stderr)
		}
		o.Log = NewLogger(w, o.LogLevel, o.LogFormat)
	}

	// One service per data directory. The lock settles the race between two
	// starts (two terminals, or the login unit plus a manual start): whoever
	// holds it serves, the other hands its folders over and exits 0.
	lock, err := tryLock(lockPath(home))
	if errors.Is(err, errLocked) {
		si, ok := waitRunning(home, 10*time.Second)
		if !ok {
			return fmt.Errorf("another groundwork holds %s but is not answering; see %s", home, LogPath(home))
		}
		return handOver(o, si)
	}
	if err != nil {
		return err
	}
	defer lock.release()
	if si, ok := Running(home); ok { // a service from a release that didn't take the lock
		return handOver(o, si)
	}

	token, err := loadToken(home)
	if err != nil {
		return err
	}
	ln, err := server.Listen(o.Host, o.Port)
	var busy *server.PortBusyError
	if errors.As(err, &busy) {
		if pid, ok := groundworkAt(busy.Port); ok {
			return fmt.Errorf("%w: another groundwork service (pid %d, probably with a different data directory) is serving there; stop it, or pass --port <n> (--port 0 picks a free one)", err, pid)
		}
		return fmt.Errorf("%w by another program; pass --port <n>, or --port 0 to pick a free one", err)
	}
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	svc, err := New(ctx, home, o.Version, o.Log)
	if err != nil {
		ln.Close()
		return err
	}
	svc.Port, svc.Commit, svc.Started, svc.LogFile = port, o.Commit, time.Now(), LogPath(home)
	o.Log.Info("groundwork service starting", "version", o.Version, "commit", o.Commit, "port", port, "home", home, "projects", len(svc.Reg.List()))
	svc.OpenAll()
	path := "/"
	for i, dir := range o.Add {
		p, _, err := svc.AddPath(dir, "")
		if err != nil {
			ln.Close()
			svc.Shutdown(context.Background())
			return err
		}
		if i == 0 {
			path = ProjectPath(p.ID)
		}
	}

	// passkeys need a secure context: http://localhost is one, a LAN address isn't
	canonical := net.JoinHostPort("localhost", strconv.Itoa(port))
	signin, err := auth.New(home, "http://"+canonical)
	if err != nil {
		ln.Close()
		svc.Shutdown(context.Background())
		return err
	}
	srv := server.New(o.Log, nil, server.Info{Root: home, Version: o.Version}, token)
	srv.SetAPI(svc.Routes)
	srv.SetAuth(signin)
	srv.AllowPort(port)
	if isLoopback(o.Host) {
		srv.SetCanonicalHost(canonical)
	}
	if o.AllowRemote && !isLoopback(o.Host) {
		srv.AllowHost(net.JoinHostPort(o.Host, strconv.Itoa(port)))
		fmt.Fprintln(os.Stderr, "WARNING: groundwork is reachable beyond localhost. Anyone who knows the password can run commands on this machine; set sign-in up at http://"+canonical+" first.")
	}
	if !signin.Configured() {
		o.Log.Info("browser sign-in is not set up yet; the first visit from this machine sets it up")
	}

	si := ServerInfo{PID: os.Getpid(), Port: port, Version: o.Version, Token: token}
	infoPath := filepath.Join(home, "server.json")
	if err := writeJSONFile(infoPath, si); err != nil {
		return err
	}
	defer os.Remove(infoPath)
	// which release last ran against this data directory, for bug reports and
	// for a future release that has to migrate it
	_ = os.WriteFile(filepath.Join(home, "version"), []byte(o.Version+"\n"), 0o600)

	// ReadHeaderTimeout bounds slow clients; no ReadTimeout/WriteTimeout, since
	// SSE streams and long file uploads are legitimate. IdleTimeout reaps
	// keep-alive connections nothing uses.
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	fmt.Fprintf(o.Out, "groundwork service (%d projects, data in %s)\n", len(svc.Reg.List()), home)
	fmt.Fprintln(o.Out, URLFor(browserHost(o.Host), si, path))
	if !o.NoOpen {
		_ = browser.OpenURL(URLFor(browserHost(o.Host), si, path))
	}

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-sigCtx.Done():
	}
	if n := svc.Active(); n > 0 {
		fmt.Fprintf(o.Out, "%d run(s) in progress; stopping them gracefully…\n", n)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = httpSrv.Shutdown(shutdownCtx)
	cancel()
	stopCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	svc.Shutdown(stopCtx)
	return nil
}

// NewLogger builds the service logger. level is debug|info|warn|error
// (default info; GROUNDWORK_LOG is read by the CLI), format text|json.
func NewLogger(w io.Writer, level, format string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	if strings.EqualFold(format, "json") {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// handOver is what a second start does when a service is already running:
// imports the folders it was given there and prints or opens the URL.
func handOver(o Options, si ServerInfo) error {
	path := "/"
	for i, dir := range o.Add {
		p, err := NewClient(si).AddPath(dir)
		if err != nil {
			return err
		}
		if i == 0 {
			path = ProjectPath(p.ID)
		}
	}
	fmt.Fprintln(o.Out, "groundwork is already running:", si.URL(path))
	if !o.NoOpen {
		_ = browser.OpenURL(si.URL(path))
	}
	return nil
}

// waitRunning polls Running for up to d: a service that holds the lock may
// still be starting.
func waitRunning(home string, d time.Duration) (ServerInfo, bool) {
	deadline := time.Now().Add(d)
	for {
		if si, ok := Running(home); ok {
			return si, true
		}
		if time.Now().After(deadline) {
			return ServerInfo{}, false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// groundworkAt reports whether a groundwork service answers on port, and its pid.
func groundworkAt(port int) (int, bool) {
	c := http.Client{Timeout: time.Second}
	resp, err := c.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/healthz")
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	var body struct {
		App string
		PID int
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil || body.App != "groundwork" {
		return 0, false
	}
	return body.PID, true
}

// loadToken keeps the CLI's token across restarts. Signed-in browsers also
// get it (in the page) as the API's anti-forgery header.
func loadToken(home string) (string, error) {
	if t := readToken(home); t != "" {
		return t, nil
	}
	t, err := server.NewToken()
	if err != nil {
		return "", err
	}
	return t, os.WriteFile(filepath.Join(home, "token"), []byte(t+"\n"), 0o600)
}

// readToken returns the saved session token, or "" if there is none yet.
func readToken(home string) string {
	b, err := os.ReadFile(filepath.Join(home, "token"))
	if err != nil {
		return ""
	}
	if t := strings.TrimSpace(string(b)); len(t) == 64 {
		return t
	}
	return ""
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeJSONFile(path string, v any) error {
	b, _ := json.Marshal(v)
	return os.WriteFile(path, b, 0o600)
}

// Running reports whether <home>/server.json points at a live service.
func Running(home string) (ServerInfo, bool) {
	var si ServerInfo
	b, err := os.ReadFile(filepath.Join(home, "server.json"))
	if err != nil || json.Unmarshal(b, &si) != nil || si.Port == 0 {
		return si, false
	}
	si.Token = readToken(home)
	c := http.Client{Timeout: time.Second}
	resp, err := c.Get(si.Base() + "/healthz")
	if err != nil {
		return si, false
	}
	defer resp.Body.Close()
	var body struct{ App string }
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return si, false
	}
	return si, resp.StatusCode == 200 && body.App == "groundwork"
}
