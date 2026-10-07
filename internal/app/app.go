// Package app wires config, storage and the HTTP server together.
package app

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
	"syscall"
	"time"

	"github.com/pkg/browser"

	"github.com/rapando/groundwork/internal/api"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/files"
	"github.com/rapando/groundwork/internal/server"
	"github.com/rapando/groundwork/internal/store"
)

const StateDir = ".groundwork"

type Options struct {
	Dir         string // starting directory (default CWD)
	Host        string
	Port        int
	NoOpen      bool
	AllowRemote bool
	Version     string
	Log         *slog.Logger
}

// ServerInfo is persisted to .groundwork/server.json so a second invocation
// in the same repo can find the running instance.
type ServerInfo struct {
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// FindRoot returns the first ancestor of dir containing .git, or dir itself.
func FindRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for d := abs; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return abs, nil
		}
		d = parent
	}
}

// Bootstrap creates .groundwork/ with a self-ignoring .gitignore so the repo's
// own files are never touched.
func Bootstrap(root string) (string, error) {
	dir := filepath.Join(root, StateDir)
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		return "", err
	}
	gi := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gi); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(gi, []byte("*\n"), 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func URL(host string, si ServerInfo) string {
	return fmt.Sprintf("http://%s/?t=%s", net.JoinHostPort(host, strconv.Itoa(si.Port)), si.Token)
}

// Run starts (or attaches to) the server and blocks until interrupted.
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
	if o.Dir == "" {
		o.Dir = "."
	}
	root, err := FindRoot(o.Dir)
	if err != nil {
		return err
	}
	stateDir, err := Bootstrap(root)
	if err != nil {
		return fmt.Errorf("bootstrap %s: %w", StateDir, err)
	}

	infoPath := filepath.Join(stateDir, "server.json")
	if si, ok := runningInstance(infoPath); ok {
		u := URL("127.0.0.1", si)
		fmt.Println("groundwork is already running for this repo:", u)
		if !o.NoOpen {
			_ = browser.OpenURL(u)
		}
		return nil
	}

	st, err := store.Open(filepath.Join(stateDir, "state.db"))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	token, err := server.NewToken()
	if err != nil {
		return err
	}
	ln, err := server.Listen(o.Host, o.Port)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	bus := events.NewBus()
	srv := server.New(o.Log, bus, server.Info{Root: root, Version: o.Version}, token)
	apiSvc := api.New(root, o.Version, bus, st)
	if err := apiSvc.Runner.Recover(500, 30*24*time.Hour); err != nil {
		o.Log.Warn("recovering interrupted runs failed", "err", err)
	}
	srv.SetAPI(apiSvc.Routes)
	bgCtx, bgStop := context.WithCancel(ctx)
	defer bgStop()
	apiSvc.Start(bgCtx)
	watcher, err := files.NewWatcher(root, apiSvc.Ignore(), 300*time.Millisecond, apiSvc.OnFilesChanged, o.Log)
	if err != nil {
		o.Log.Warn("file watching disabled", "err", err)
	} else {
		defer watcher.Close()
	}
	srv.AllowPort(port)
	if o.AllowRemote && !isLoopback(o.Host) {
		srv.AllowHost(net.JoinHostPort(o.Host, strconv.Itoa(port)))
		fmt.Fprintln(os.Stderr, "WARNING: groundwork is reachable beyond localhost. Anyone with the URL token can run commands on this machine.")
	}

	si := ServerInfo{PID: os.Getpid(), Port: port, Token: token}
	if err := writeInfo(infoPath, si); err != nil {
		return err
	}
	defer os.Remove(infoPath)

	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	u := URL(o.Host, si)
	fmt.Println("groundwork serving", root)
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
	if n := apiSvc.Runner.Active(); n > 0 {
		// terraform gets SIGINT so it can release its state lock; give it time
		fmt.Printf("%d run(s) in progress; stopping them gracefully…\n", n)
		stopCtx, stop := context.WithTimeout(context.Background(), 25*time.Second)
		apiSvc.Runner.Shutdown(stopCtx)
		stop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeInfo(path string, si ServerInfo) error {
	b, _ := json.Marshal(si)
	return os.WriteFile(path, b, 0o600)
}

// runningInstance reports whether server.json points at a live groundwork.
func runningInstance(path string) (ServerInfo, bool) {
	var si ServerInfo
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &si) != nil || si.Port == 0 {
		return si, false
	}
	c := http.Client{Timeout: time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", si.Port))
	if err != nil {
		return si, false
	}
	defer resp.Body.Close()
	var body struct{ App string }
	json.NewDecoder(resp.Body).Decode(&body)
	return si, resp.StatusCode == 200 && body.App == "groundwork"
}
