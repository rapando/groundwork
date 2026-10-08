package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Add         []string // folders to import at startup; the browser opens the first
}

// ServerInfo is persisted to <home>/server.json so other invocations (the
// CLI, a second `serve`) can find the running service.
type ServerInfo struct {
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
	Token string `json:"token"`
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

	if si, ok := Running(home); ok {
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
		u := si.URL(path)
		fmt.Println("groundwork is already running:", u)
		if !o.NoOpen {
			_ = browser.OpenURL(u)
		}
		return nil
	}

	token, err := loadToken(home)
	if err != nil {
		return err
	}
	ln, err := server.Listen(o.Host, o.Port)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	svc, err := New(ctx, home, o.Version, o.Log)
	if err != nil {
		ln.Close()
		return err
	}
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

	si := ServerInfo{PID: os.Getpid(), Port: port, Token: token}
	infoPath := filepath.Join(home, "server.json")
	if err := writeJSONFile(infoPath, si); err != nil {
		return err
	}
	defer os.Remove(infoPath)

	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	u := URLFor(o.Host, si, path)
	fmt.Printf("groundwork service (%d projects, data in %s)\n", len(svc.Reg.List()), home)
	fmt.Println(u)
	if !o.NoOpen {
		_ = browser.OpenURL(u)
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
		fmt.Printf("%d run(s) in progress; stopping them gracefully…\n", n)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = httpSrv.Shutdown(shutdownCtx)
	cancel()
	stopCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	svc.Shutdown(stopCtx)
	return nil
}

// loadToken keeps the session token across restarts so open tabs and
// bookmarks survive the service restarting at login.
func loadToken(home string) (string, error) {
	p := filepath.Join(home, "token")
	if b, err := os.ReadFile(p); err == nil {
		if t := strings.TrimSpace(string(b)); len(t) == 64 {
			return t, nil
		}
	}
	t, err := server.NewToken()
	if err != nil {
		return "", err
	}
	return t, os.WriteFile(p, []byte(t+"\n"), 0o600)
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
