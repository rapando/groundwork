package runner

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rapando/groundwork/v2/internal/store"
)

// A run that outlives run_timeout is stopped and fails with the reason; the
// root is free again afterwards.
func TestRunTimeoutStopsAHungRun(t *testing.T) {
	g := newRig(t)
	g.cfg.Terraform.RunTimeout = "1s"
	t.Setenv("FAKE_PLAN_DELAY", "20")
	start := time.Now()
	run, err := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	d := g.wait(t, run.ID, store.StatusFailed)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("took %s to time out", time.Since(start))
	}
	if !strings.Contains(d.Summary.Error, "timeout of 1s") {
		t.Fatalf("summary error: %q", d.Summary.Error)
	}
	if d.Target.RunTimeout != "1s" {
		t.Fatalf("run detail should show the timeout: %+v", d.Target)
	}
	if d.Run.PGID != 0 {
		t.Fatalf("pgid should be cleared once the tool exited: %d", d.Run.PGID)
	}
	// the root is not left locked
	t.Setenv("FAKE_PLAN_DELAY", "0")
	run, _ = g.r.Submit(SubmitRequest{Kind: KindInit, Root: "terraform/envs/dev", Env: "dev"})
	g.wait(t, run.ID, store.StatusSucceeded)
	// a cancel by a person is still "cancelled", not a timeout
	t.Setenv("FAKE_PLAN_DELAY", "20")
	g.cfg.Terraform.RunTimeout = ""
	run, _ = g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	g.wait(t, run.ID, store.StatusRunning)
	time.Sleep(200 * time.Millisecond)
	if err := g.r.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	g.wait(t, run.ID, store.StatusCancelled)
}

// After a hard stop (no chance to interrupt the tool), Recover notices a
// process group recorded on an interrupted run that is still alive and
// raises an issue naming it; a dead one raises nothing.
func TestRecoverReportsOrphanedProcessGroups(t *testing.T) {
	g := newRig(t)
	orphan := exec.Command("sleep", "30")
	orphan.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Kill(-orphan.Process.Pid, syscall.SIGKILL); orphan.Wait() })

	alive, _ := g.st.CreateRun(KindPlan, Target{Root: "terraform/envs/dev", Env: "dev"}, nil, "")
	pg := orphan.Process.Pid
	g.st.UpdateRun(alive.ID, store.RunUpdate{Status: store.StatusRunning, PGID: &pg})
	dead, _ := g.st.CreateRun(KindPlan, Target{Root: "terraform/envs/prod", Env: "prod"}, nil, "")
	gone := 999999
	g.st.UpdateRun(dead.ID, store.RunUpdate{Status: store.StatusRunning, PGID: &gone})

	if err := g.r.Recover(500, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	issues, err := g.st.ListIssues("open", time.Time{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found *store.Issue
	for i := range issues {
		if issues[i].RuleID == "groundwork-orphaned-process" {
			if issues[i].Target == "terraform/envs/prod" {
				t.Fatal("a dead process group is not an orphan")
			}
			found = &issues[i]
		}
	}
	if found == nil || found.Target != "terraform/envs/dev" || !strings.Contains(string(found.Context), `"pgid":"`+strconv.Itoa(pg)+`"`) {
		t.Fatalf("orphan issue: %+v", found)
	}
	for _, id := range []int64{alive.ID, dead.ID} {
		if r, _ := g.st.GetRun(id); r.Status != store.StatusFailed {
			t.Fatalf("interrupted run %d should be failed: %s", id, r.Status)
		}
	}
}
