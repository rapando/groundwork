package service

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
