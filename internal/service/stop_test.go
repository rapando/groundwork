package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

type fakeUnit struct {
	svcManager
	stopErr error
	stopped bool
}

func (f *fakeUnit) installed() bool { return true }
func (f *fakeUnit) stop() error     { f.stopped = true; return f.stopErr }

// fakeService stands in for a running `groundwork serve`: a process that
// SIGTERM ends, and a /healthz that answers only while it lives.
func fakeService(t *testing.T, home string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() { cmd.Process.Kill() })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-done:
			w.WriteHeader(503)
		default:
			w.Write([]byte(`{"app":"groundwork"}`))
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	if err := writeJSONFile(filepath.Join(home, "server.json"), ServerInfo{PID: cmd.Process.Pid, Port: port, Token: tok}); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func withUnit(t *testing.T, u *fakeUnit) {
	old := manager
	manager = func() svcManager { return u }
	t.Cleanup(func() { manager = old })
}

// The login service is installed but not loaded (`launchctl bootout` says
// "No such process") while a service started by a plain `groundwork` runs:
// stop must still stop that one.
func TestStopReachesAServiceStartedOutsideTheUnit(t *testing.T) {
	home := t.TempDir()
	cmd := fakeService(t, home)
	u := &fakeUnit{stopErr: errors.New("Boot-out failed: 3: No such process")}
	withUnit(t, u)
	if err := Stop(home); err != nil {
		t.Fatal(err)
	}
	if !u.stopped {
		t.Error("the unit must be stopped first, so launchd/systemd don't restart it")
	}
	if _, ok := Running(home); ok {
		t.Fatal("still running")
	}
	if cmd.ProcessState == nil || cmd.ProcessState.Success() {
		t.Fatalf("the process wasn't signalled: %v", cmd.ProcessState)
	}
}

func TestStopWithNothingRunning(t *testing.T) {
	withUnit(t, &fakeUnit{}) // the unit stopped (it was the service), nothing answers: done
	if err := Stop(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	withUnit(t, &fakeUnit{stopErr: errors.New("not loaded")})
	if err := Stop(t.TempDir()); err == nil || err.Error() != "groundwork service is not running" {
		t.Fatalf("%v", err)
	}
}
