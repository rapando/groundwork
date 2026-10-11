package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rapando/groundwork/internal/service"
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
