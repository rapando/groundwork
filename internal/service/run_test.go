package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startRun runs the service on a free port in the background with a fresh
// home and returns its captured output; cancelling ctx stops it.
func startRun(t *testing.T, home string, o Options) (*bytes.Buffer, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	o.Home, o.Port, o.NoOpen, o.Log, o.Out = home, 0, true, quiet(), &out
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := Running(home); ok {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Run returned early: %v\n%s", err, out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("service did not start:\n%s", out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	return &out, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("Run did not return after cancel")
		}
	}
}

// A login service's stdout is a log file: the token must never be printed
// there, and server.json (readable by anything that finds the data dir path
// in a bug report) must not carry it either.
func TestServeOutputAndServerInfoCarryNoToken(t *testing.T) {
	home := t.TempDir()
	out, stop := startRun(t, home, Options{})
	defer stop()

	si, ok := Running(home)
	if !ok || si.Token == "" {
		t.Fatalf("Running = %+v, %v; want a token read from <home>/token", si, ok)
	}
	if s := out.String(); strings.Contains(s, "?t=") || strings.Contains(s, si.Token) {
		t.Fatalf("output leaks the token:\n%s", s)
	}
	if !strings.Contains(out.String(), si.Base()+"/") || !strings.Contains(out.String(), "groundwork open") {
		t.Fatalf("output should print the plain URL and how to sign in:\n%s", out.String())
	}
	raw, err := os.ReadFile(filepath.Join(home, "server.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["token"]; has || strings.Contains(string(raw), si.Token) {
		t.Fatalf("server.json carries the token: %s", raw)
	}
	tok, _ := os.ReadFile(filepath.Join(home, "token"))
	if strings.TrimSpace(string(tok)) != si.Token {
		t.Fatal("Running should read the token from <home>/token")
	}
}

// The data directory records which release wrote it, and the CLI learns the
// service's version from server.json so it can warn about a mismatch.
func TestServeRecordsItsVersion(t *testing.T) {
	home := t.TempDir()
	_, stop := startRun(t, home, Options{Version: "1.2.3"})
	defer stop()
	si, _ := Running(home)
	if si.Version != "1.2.3" {
		t.Fatalf("server.json version = %q", si.Version)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "version")); strings.TrimSpace(string(b)) != "1.2.3" {
		t.Fatalf("<home>/version = %q", b)
	}
}

// At a terminal the signed-in URL is what the user wants.
func TestServeAtATerminalPrintsTheSignedInURL(t *testing.T) {
	home := t.TempDir()
	out, stop := startRun(t, home, Options{ShowToken: true})
	defer stop()
	si, _ := Running(home)
	if !strings.Contains(out.String(), si.URL("/")) {
		t.Fatalf("want %s in:\n%s", si.URL("/"), out.String())
	}
}

// Two starts against one data directory: the second sees the first's lock,
// hands over and exits 0 without serving. The lock is gone once the first
// stops, so a restart works.
func TestSecondServeHandsOverToTheFirst(t *testing.T) {
	home := t.TempDir()
	_, stop := startRun(t, home, Options{})
	first, _ := Running(home)

	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), Options{Home: home, Port: 0, NoOpen: true, Log: quiet(), Out: &out}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second serve: %v\n%s", err, out.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("second serve did not exit")
	}
	if !strings.Contains(out.String(), "already running") || !strings.Contains(out.String(), first.Base()) {
		t.Fatalf("second serve output:\n%s", out.String())
	}
	if si, ok := Running(home); !ok || si.PID != first.PID || si.Port != first.Port {
		t.Fatalf("the first service should still be the one: %+v %v (first %+v)", si, ok, first)
	}
	stop()
	if _, ok := Running(home); ok {
		t.Fatal("still running after stop")
	}
	l, err := tryLock(lockPath(home))
	if err != nil {
		t.Fatalf("lock not released after the service stopped: %v", err)
	}
	l.release()
	_, stop2 := startRun(t, home, Options{})
	stop2()
}

// A busy port is an error that says who holds it; it is never silently
// replaced, since bookmarks and scripts rely on it. --port 0 picks a free one.
func TestBusyPortIsAnErrorNotAFallback(t *testing.T) {
	// another groundwork (a different data directory) on the port
	otherHome := t.TempDir()
	_, stop := startRun(t, otherHome, Options{})
	defer stop()
	other, _ := Running(otherHome)
	var out bytes.Buffer
	err := Run(context.Background(), Options{Home: t.TempDir(), Port: other.Port, NoOpen: true, Log: quiet(), Out: &out})
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(other.Port)) || !strings.Contains(err.Error(), "another groundwork") || !strings.Contains(err.Error(), strconv.Itoa(other.PID)) {
		t.Fatalf("got %v", err)
	}
	// some other program on the port
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	err = Run(context.Background(), Options{Home: t.TempDir(), Port: port, NoOpen: true, Log: quiet(), Out: &out})
	if err == nil || !strings.Contains(err.Error(), "another program") || !strings.Contains(err.Error(), "--port 0") {
		t.Fatalf("got %v", err)
	}
}
