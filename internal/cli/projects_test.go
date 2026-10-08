package cli

import (
	"os"
	"path/filepath"
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
