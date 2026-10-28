package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rapando/groundwork/v2/internal/service"
)

func TestFindProjectNeverFallsBackToTheEnclosingRepo(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "demo")
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	real, _ := filepath.EvalSymlinks(repo)
	list := []service.ProjectView{
		{Project: service.Project{ID: "demo-aaaaaa", Name: "demo", Path: real}},
		{Project: service.Project{ID: "infra-bbbbbb", Name: "infra", Path: "/elsewhere/infra"}},
	}
	t.Chdir(repo)
	if _, err := findProject(list, "terraform-only"); err == nil {
		t.Fatal("an unknown name matched the project in the working directory")
	}
	for ref, want := range map[string]string{"infra": "infra-bbbbbb", "demo-aaaaaa": "demo-aaaaaa", ".": "demo-aaaaaa"} {
		if p, err := findProject(list, ref); err != nil || p.ID != want {
			t.Errorf("findProject(%q) = %v, %v; want %s", ref, p.ID, err, want)
		}
	}
}

func TestVersionMismatchMessage(t *testing.T) {
	cases := map[string]bool{"": false, "dev": false, "1.2.3": false, "1.2.4": true}
	for svc, want := range cases {
		if got := versionMismatch(service.ServerInfo{Version: svc}, "1.2.3"); (got != "") != want {
			t.Errorf("service %q vs cli 1.2.3: %q", svc, got)
		}
	}
	if versionMismatch(service.ServerInfo{Version: "1.2.4"}, "dev") != "" {
		t.Fatal("a dev build should not nag")
	}
	if m := versionMismatch(service.ServerInfo{Version: "1.2.4"}, "1.2.3"); !strings.Contains(m, "groundwork service restart") {
		t.Fatalf("should say how to fix it: %s", m)
	}
}

// An installed login service that fails to start falls back to a background
// service, and says why instead of suggesting `service install`.
func TestEnsureServiceSaysWhyTheLoginServiceDidntStart(t *testing.T) {
	t.Setenv("GROUNDWORK_HOME", t.TempDir())
	oldInstalled, oldBackground := startInstalled, startBackground
	t.Cleanup(func() { startInstalled, startBackground = oldInstalled, oldBackground })
	background := 0
	startBackground = func(string, int) (service.ServerInfo, error) {
		background++
		return service.ServerInfo{Port: 7420}, nil
	}
	cases := []struct {
		name      string
		installed bool
		err       error
		want      string
		notWant   string
	}{
		{"not installed", false, nil, "`groundwork service install` starts it at login", "didn't start"},
		{"installed, failed", true, errors.New("Bootstrap failed: 5: Input/output error"),
			"the login service didn't start (Bootstrap failed: 5: Input/output error)", "groundwork service install"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			startInstalled = func(string) (bool, error) { return c.installed, c.err }
			before := background
			var stderr bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetErr(&stderr)
			if _, si, err := ensureService(cmd, 7420); err != nil || si.Port != 7420 {
				t.Fatalf("ensureService = %v, %v", si, err)
			}
			if background != before+1 {
				t.Error("didn't fall back to a background service")
			}
			out := stderr.String()
			if !strings.Contains(out, c.want) || strings.Contains(out, c.notWant) {
				t.Errorf("stderr = %q; want %q and not %q", out, c.want, c.notWant)
			}
		})
	}
}
