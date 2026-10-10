package diagnostics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/runner"
	"github.com/rapando/groundwork/internal/store"
)

type rig struct {
	e    *Engine
	r    *runner.Runner
	st   *store.Store
	lock string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "envs/dev"), 0o755)
	cfg := &config.Config{Version: 1, Mode: "standalone", Terraform: config.Terraform{Binary: "terraform", Roots: []config.TFRoot{{Path: "envs/dev", Env: "dev"}}}}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bus := events.NewBus()
	rn := runner.New(root, st, bus, func() *config.Config { return cfg }, nil)
	t.Cleanup(func() { rn.Shutdown(context.Background()) })
	fake, _ := filepath.Abs("../../testdata/fake-terraform")
	data, _ := filepath.Abs("../../testdata/terraform")
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DATA", data)
	lock := filepath.Join(t.TempDir(), "locked")
	t.Setenv("FAKE_LOCKED", lock)
	e := New(root, st, rn, nil, bus, nil)
	e.PS = func() (string, error) { return "  1 /sbin/launchd\n 42 /usr/bin/ssh-agent\n", nil }
	return &rig{e, rn, st, lock}
}

func (g *rig) run(t *testing.T, req runner.SubmitRequest, want ...string) int64 {
	t.Helper()
	run, err := g.r.Submit(req)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		d, _ := g.r.Detail(run.ID)
		for _, s := range want {
			if d.Run.Status == s {
				g.e.ProcessRun(run.ID)
				return run.ID
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("run %d never reached %v", run.ID, want)
	return 0
}

// A failed plan opens a stale-lock issue; unlocking from the issue (typed
// confirmation, pre-checks) and re-planning resolves it.
func TestStaleLockLifecycle(t *testing.T) {
	g := newRig(t)
	os.WriteFile(g.lock, nil, 0o644)
	failed := g.run(t, runner.SubmitRequest{Kind: runner.KindPlan, Root: "envs/dev", Env: "dev"}, store.StatusFailed)

	open, _ := g.st.ListIssues(store.IssueOpen, time.Time{}, 10)
	if len(open) != 1 || open[0].RuleID != "tf-state-lock-stale" || open[0].Target != "tf:envs/dev@dev" {
		t.Fatalf("%+v", open)
	}
	is := open[0]
	v := g.e.View(is)
	if v.Title != "State locked on envs/dev (dev)" || v.Facts["lock_id"] != "eb4ca245-50b1-5658-c0c3-a4c69baa2888" || v.RunID != failed {
		t.Fatalf("%+v", v)
	}
	if v.Steps[0].Action.Command != "terraform -chdir=envs/dev force-unlock -force eb4ca245-50b1-5658-c0c3-a4c69baa2888" {
		t.Errorf("%q", v.Steps[0].Action.Command)
	}
	ck := g.e.Prechecks(is)
	if len(ck) != 2 || !ck[0].OK || !ck[1].OK {
		t.Fatalf("%+v", ck)
	}
	// re-processing the same run doesn't duplicate
	g.e.done = map[int64]string{}
	g.e.ProcessRun(failed)
	if all, _ := g.st.ListIssues("", time.Time{}, 10); len(all) != 1 {
		t.Fatalf("%d issues", len(all))
	}

	if _, err := g.e.Act(is, 0, "prod"); !errors.Is(err, ErrConfirm) {
		t.Fatalf("wrong confirmation accepted: %v", err)
	}
	replan, err := g.e.Act(is, 1, "") // step 2 (plan) isn't mutating; the lock is still there, so it fails
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.e.Act(is, 0, "dev"); err == nil { // the pre-check sees the running plan
		t.Fatal("force-unlock must wait for the running plan")
	}
	g.wait(t, replan.ID, store.StatusFailed)
	unlock, err := g.e.Act(is, 0, "dev")
	if err != nil {
		t.Fatal(err)
	}
	g.wait(t, unlock.ID, store.StatusSucceeded)
	if _, err := os.Stat(g.lock); !os.IsNotExist(err) {
		t.Fatal("fake lock still present: force-unlock didn't run")
	}
	got, _ := g.st.GetIssue(is.ID)
	if got.Status != store.IssueResolved || got.Resolution == "" {
		t.Fatalf("%+v", got)
	}

	// a later failure reopens it; re-reading the old failed run doesn't
	g.e.done = map[int64]string{}
	g.e.ProcessRun(failed)
	if got, _ := g.st.GetIssue(is.ID); got.Status != store.IssueResolved {
		t.Fatal("old evidence re-opened a resolved issue")
	}
	os.WriteFile(g.lock, nil, 0o644)
	g.run(t, runner.SubmitRequest{Kind: runner.KindPlan, Root: "envs/dev", Env: "dev"}, store.StatusFailed)
	if got, _ := g.st.GetIssue(is.ID); got.Status != store.IssueOpen {
		t.Fatal("new failure should re-open the issue")
	}
}

func (g *rig) wait(t *testing.T, id int64, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		d, _ := g.r.Detail(id)
		if d.Run.Status == want {
			g.e.ProcessRun(id)
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("run %d never reached %s", id, want)
}

func TestPrechecksBlockMutatingActions(t *testing.T) {
	g := newRig(t)
	os.WriteFile(g.lock, nil, 0o644)
	g.run(t, runner.SubmitRequest{Kind: runner.KindPlan, Root: "envs/dev", Env: "dev"}, store.StatusFailed)
	open, _ := g.st.ListIssues(store.IssueOpen, time.Time{}, 10)
	g.e.PS = func() (string, error) { return "  1 /sbin/launchd\n 977 /opt/homebrew/bin/terraform\n", nil }
	ck := g.e.Prechecks(open[0])
	if ck[1].OK || ck[1].Detail == "" {
		t.Fatalf("%+v", ck)
	}
	if _, err := g.e.Act(open[0], 0, "dev"); err == nil {
		t.Fatal("force-unlock must not run while terraform is running locally")
	}
}

func TestManualResolveAndDiagnosticsLifecycle(t *testing.T) {
	g := newRig(t)
	g.st.UpsertIssue("tf-missing-variable", "tf:envs/dev@dev", "alert_email", "run", time.Now(), Context{Target: "envs/dev (dev)", Env: "dev", Vars: map[string]string{"variable": "alert_email"}})
	open, _ := g.st.ListIssues(store.IssueOpen, time.Time{}, 10)
	if v := g.e.View(open[0]); v.Title != "alert_email has no value in dev" {
		t.Fatalf("%q", v.Title)
	}
	if ok, _ := g.st.ResolveIssue(open[0].ID, time.Now(), "marked resolved"); !ok {
		t.Fatal("resolve")
	}
	if g.st.CountResolvedSince(time.Now().Add(-time.Hour)) != 1 {
		t.Fatal("count")
	}
}
