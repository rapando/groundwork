package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rapando/groundwork/internal/service"
)

// runRoot executes the command tree with the given stdin and returns stdout.
func runRoot(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@e.com"}, {"config", "user.name", "t"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	t.Chdir(dir)
	return dir
}

func TestInitScaffoldsWritesFilesAndCommits(t *testing.T) {
	dir := gitRepo(t)
	t.Setenv("GROUNDWORK_HOME", t.TempDir())
	// without --yes, answering "n" writes nothing
	out, err := runRoot(t, "n\n", "init", "--preset", "onprem", "--project", "demo")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "groundwork.yaml")); err == nil {
		t.Fatal("declined scaffold wrote files")
	}
	// --dry-run shows the plan and writes nothing either
	out, err = runRoot(t, "", "init", "--preset", "onprem", "--project", "demo", "--dry-run")
	if err != nil || !strings.Contains(out, "groundwork.yaml") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "groundwork.yaml")); err == nil {
		t.Fatal("dry run wrote files")
	}
	// for real, and committed
	out, err = runRoot(t, "", "init", "--preset", "onprem", "--project", "demo", "--yes", "--git-commit", "--envs", "dev,prod", "--vault")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "committed: chore: scaffold with groundwork") {
		t.Fatalf("no commit message in output:\n%s", out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "groundwork.yaml"))
	if err != nil || !strings.Contains(string(b), "version: 1") {
		t.Fatalf("config: %v %s", err, b)
	}
	log, _ := exec.Command("git", "-C", dir, "log", "--oneline").Output()
	if !strings.Contains(string(log), "scaffold with groundwork") {
		t.Fatalf("not committed: %s", log)
	}
	// a bad preset and a bad flag combination are refused before anything is written
	if _, err := runRoot(t, "", "init", "--preset", "nope", "--yes"); err == nil {
		t.Fatal("unknown preset accepted")
	}
}

func TestInitGitCommitOutsideARepository(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GROUNDWORK_HOME", t.TempDir())
	_, err := runRoot(t, "", "init", "--preset", "onprem", "--project", "demo", "--yes", "--git-commit")
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "groundwork.yaml")); err != nil {
		t.Fatal("the files should still have been written; only the commit failed")
	}
}

// drift runs a refresh-only plan per environment with the fake terraform and
// reports per target; exit 1 when anything drifted, 2 for an unknown env.
func TestDriftCommandReportsPerEnvironment(t *testing.T) {
	fake, _ := filepath.Abs("../../testdata/fake-terraform") // before repo() changes directory
	data, _ := filepath.Abs("../../testdata/terraform")
	dir := repo(t, true)
	t.Setenv("GROUNDWORK_HOME", t.TempDir())
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DATA", data)
	out, err := runRoot(t, "", "drift")
	if code(err) != 1 {
		log, _ := os.ReadFile(filepath.Join(dir, ".groundwork", "runs", "1", "log.ndjson"))
		t.Fatalf("want exit 1 (drift), got %v\n%s\nrun log:\n%s", err, out, log)
	}
	if !strings.Contains(out, "DRIFT   terraform/envs/dev (dev)") || !strings.Contains(out, "(prod)") {
		t.Fatalf("report:\n%s", out)
	}
	t.Setenv("FAKE_DRIFT_EXIT", "0")
	out, err = runRoot(t, "", "drift", "--env", "dev")
	if err != nil || !strings.Contains(out, "OK      terraform/envs/dev (dev): no drift") || strings.Contains(out, "(prod)") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := runRoot(t, "", "drift", "--env", "bogus"); code(err) != 2 {
		t.Fatalf("unknown env: %v", err)
	}
}

// startService runs a service in this process for the CLI to talk to. The
// returned stop must be deferred by the caller: t.Cleanup runs last-in
// first-out, so temp dirs made after this call (the projects the service is
// writing into) would otherwise be removed while it still runs.
func startService(t *testing.T) (service.ServerInfo, func()) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GROUNDWORK_HOME", home)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- service.Run(ctx, service.Options{Home: home, Port: 0, NoOpen: true, Version: Version, Out: new(bytes.Buffer),
			Log: service.NewLogger(new(bytes.Buffer), "warn", "text")})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if si, ok := service.Running(home); ok {
			var once sync.Once
			stop := func() { once.Do(func() { cancel(); <-done }) }
			t.Cleanup(stop)
			return si, stop
		}
		if time.Now().After(deadline) {
			t.Fatal("service did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProjectCommandsAgainstARunningService(t *testing.T) {
	si, stop := startService(t)
	defer stop()
	t.Setenv("HOME", t.TempDir())
	out, err := runRoot(t, "", "projects")
	if err != nil || !strings.Contains(out, "no projects yet") {
		t.Fatalf("%v\n%s", err, out)
	}
	dir := repo(t, true)
	out, err = runRoot(t, "", "add", dir)
	if err != nil || !strings.Contains(out, si.Base()+"/p/") {
		t.Fatalf("add: %v\n%s", err, out)
	}
	id := strings.Fields(out)[0]
	out, err = runRoot(t, "", "projects")
	if err != nil || !strings.Contains(out, id) || !strings.Contains(out, "ID") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	out, err = runRoot(t, "", "open", "--no-open", id)
	if err != nil || !strings.Contains(out, "/p/"+id+"/?t=") {
		t.Fatalf("open: %v\n%s", err, out)
	}
	out, err = runRoot(t, "", "open", "--no-open")
	if err != nil || !strings.Contains(out, si.Base()+"/?t=") {
		t.Fatalf("open list: %v\n%s", err, out)
	}
	out, err = runRoot(t, "", "service", "status")
	if err != nil || !strings.Contains(out, "status:        running") || !strings.Contains(out, "projects:      1 available") {
		t.Fatalf("status: %v\n%s", err, out)
	}
	out, err = runRoot(t, "", "service", "start")
	if err != nil || !strings.Contains(out, "running:") {
		t.Fatalf("start while running: %v\n%s", err, out)
	}
	if _, err := runRoot(t, "", "remove", "nope"); err == nil || !strings.Contains(err.Error(), "no project matches") {
		t.Fatalf("remove unknown: %v", err)
	}
	out, err = runRoot(t, "", "remove", filepath.Base(dir))
	if err != nil || !strings.Contains(out, "removed "+id) {
		t.Fatalf("remove by name: %v\n%s", err, out)
	}
	// plain `groundwork` in a repository imports it and prints the URL
	t.Chdir(dir)
	out, err = runRoot(t, "", "--no-open")
	if err != nil || !strings.Contains(out, "/p/") {
		t.Fatalf("root: %v\n%s", err, out)
	}
	if _, err := runRoot(t, "", "add", "/"); err == nil {
		t.Fatal("importing / should be refused")
	}
}

func TestIsRemote(t *testing.T) {
	for s, want := range map[string]bool{
		"https://github.com/a/b.git": true, "ssh://git@h/a/b": true, "git@github.com:a/b.git": true,
		"/tmp/x": false, "./x": false, "../x": false, "infra": false, "user@host": false,
	} {
		if isRemote(s) != want {
			t.Errorf("isRemote(%q) = %v", s, !want)
		}
	}
}
