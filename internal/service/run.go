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

	"github.com/rapando/groundwork/internal/server"
)

type Options struct {
	Home        string // default Home()
	Host        string
	Port        int
	NoOpen      bool
	AllowRemote bool
	Version     string
	Log         *slog.Logger
	Add         []string  // folders to import at startup; the browser opens the first
	Out         io.Writer // startup messages; default os.Stdout
	// ShowToken includes the session token in the printed URL. The CLI sets
	// it only when stdout is a terminal: a login service's stdout is a log
	// file, and the token must never land in one.
	ShowToken bool
}

// ServerInfo is persisted to <home>/server.json so other invocations (the
// CLI, a second `serve`) can find the running service. The token is not part
// of the file: Running reads it from <home>/token.
type ServerInfo struct {
	PID     int    `json:"pid"`
	Port    int    `json:"port"`
	Version string `json:"version,omitempty"` // the binary serving; the CLI warns when it differs from its own
	Token   string `json:"-"`
}

func (si ServerInfo) Base() string { return "http://127.0.0.1:" + strconv.Itoa(si.Port) }

// URL is the address that logs a browser in: path plus the session token.
func (si ServerInfo) URL(path string) string { return URLFor("127.0.0.1", si, path) }

func URLFor(host string, si ServerInfo, path string) string {
	if path == "" {
		path = "/"
	}
	return fmt.Sprintf("http://%s%s?t=%s", net.JoinHostPort(host, strconv.Itoa(si.Port)), path, si.Token)
}

// PlainURL is the address without the token, for output that may be logged.
func PlainURL(host string, si ServerInfo, path string) string {
	if path == "" {
		path = "/"
	}
	return fmt.Sprintf("http://%s%s", net.JoinHostPort(host, strconv.Itoa(si.Port)), path)
}

// announce is the URL line printed at startup: signed in when the output is a
// terminal, otherwise the plain address plus how to get a signed-in one.
func (o Options) announce(host string, si ServerInfo, path string) string {
	if o.ShowToken {
		return URLFor(host, si, path)
	}
	return PlainURL(host, si, path) + "  (run `groundwork open` for a signed-in URL)"
}

// ProjectPath is where a project's UI lives.
func ProjectPath(id string) string { return "/p/" + id + "/" }

// Run starts the service and blocks until interrupted. If a service is
// already running for this home, the folders in o.Add are imported into it.
func Run(ctx context.Context, o Options) error {
	if o.Log == nil {
		o.Log = slog.Default()
	}
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
	svc.Port = port
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

	srv := server.New(o.Log, nil, server.Info{Root: home, Version: o.Version}, token)
	srv.SetAPI(svc.Routes)
	srv.AllowPort(port)
	if o.AllowRemote && !isLoopback(o.Host) {
		srv.AllowHost(net.JoinHostPort(o.Host, strconv.Itoa(port)))
		fmt.Fprintln(os.Stderr, "WARNING: groundwork is reachable beyond localhost. Anyone with the URL token can run commands on this machine.")
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
	fmt.Fprintln(o.Out, o.announce(o.Host, si, path))
	if !o.NoOpen {
		_ = browser.OpenURL(URLFor(o.Host, si, path))
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
	fmt.Fprintln(o.Out, "groundwork is already running:", o.announce("127.0.0.1", si, path))
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

// loadToken keeps the session token across restarts so open tabs and
// bookmarks survive the service restarting at login.
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
