package runner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rapando/groundwork/internal/store"
)

func stateFile(t *testing.T, serial int, lineage string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "state.json")
	writeState(t, p, serial, lineage)
	t.Setenv("FAKE_STATE_FILE", p)
	return p
}

func writeState(t *testing.T, p string, serial int, lineage string) {
	t.Helper()
	os.WriteFile(p, []byte(`{"version":4,"serial":`+itoa(serial)+`,"lineage":"`+lineage+`"}`), 0o644)
}

func TestPlanRecordsWhatItWasMadeAgainst(t *testing.T) {
	g := newRig(t)
	stateFile(t, 7, "abc")
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	d := g.wait(t, run.ID, store.StatusWaitingApproval)
	s := d.Summary
	if s.State == nil || !s.State.Exists || s.State.Serial != 7 || s.State.Lineage != "abc" || s.Fingerprint == "" || s.PlannedAt == nil {
		t.Fatalf("%+v", s)
	}
	if _, err := os.Stat(filepath.Join(g.r.runDir(run.ID), "plan.json")); !os.IsNotExist(err) {
		t.Fatal("the raw show -json (which can hold secrets) must not be kept")
	}
	diff, _, err := g.r.ResourceDiff(run.ID, "module.service.terraform_data.service[0]")
	if err != nil || diff.Action != "create" || len(diff.Changed) == 0 {
		t.Fatalf("%v %+v", err, diff)
	}
	if _, _, err := g.r.ResourceDiff(run.ID, "nope.nope"); err == nil {
		t.Fatal("unknown address")
	}
}

func TestDangerousPlanNeedsAcknowledgement(t *testing.T) {
	g := newRig(t)
	t.Setenv("FAKE_SHOW_JSON", abs(t, "../../testdata/terraform/show_danger.json"))
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	d := g.wait(t, run.ID, store.StatusWaitingApproval)
	if len(d.Summary.Danger) != 2 {
		t.Fatalf("%+v", d.Summary.Danger)
	}
	if err := g.r.Approve(run.ID, ApproveRequest{}); !errors.Is(err, ErrNeedsAck) {
		t.Fatalf("%v", err)
	}
	if d, _ := g.r.Detail(run.ID); d.Run.Status != store.StatusWaitingApproval {
		t.Fatal("refusal must leave the plan waiting")
	}
	if err := g.r.Approve(run.ID, ApproveRequest{AckDanger: true}); err != nil {
		t.Fatal(err)
	}
	d = g.wait(t, run.ID, store.StatusSucceeded)
	if d.Approval == nil || !d.Approval.AckDanger {
		t.Fatalf("acknowledgement must be recorded: %+v", d.Approval)
	}
	// the log explains the danger too
	p, _ := g.r.ReadLog(run.ID, 0, 1000, "warn", "aws_db_instance.main")
	if len(p.Lines) == 0 {
		t.Fatal("danger should be logged as a warning")
	}
}

func TestStateChangeAfterPlanBlocksApproval(t *testing.T) {
	g := newRig(t)
	sf := stateFile(t, 7, "abc")
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	g.wait(t, run.ID, store.StatusWaitingApproval)

	writeState(t, sf, 8, "abc") // someone applied elsewhere
	err := g.r.Approve(run.ID, ApproveRequest{})
	var se *StaleError
	if !errors.As(err, &se) || !errors.Is(err, ErrStale) || !strings.Contains(err.Error(), "serial 7 → 8") {
		t.Fatalf("%v", err)
	}
	if d, _ := g.r.Detail(run.ID); d.Run.Status != store.StatusWaitingApproval || d.Approval != nil {
		t.Fatal("a refused approval leaves no trace")
	}
	writeState(t, sf, 7, "other") // state replaced, same serial
	if err := g.r.Approve(run.ID, ApproveRequest{}); err == nil || !strings.Contains(err.Error(), "lineage") {
		t.Fatalf("%v", err)
	}
	os.Remove(sf)
	if err := g.r.Approve(run.ID, ApproveRequest{}); err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("%v", err)
	}

	// re-plan: the old plan is discarded and a new one starts
	writeState(t, sf, 8, "abc")
	nr, err := g.r.Replan(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := g.r.Detail(run.ID); d.Run.Status != store.StatusCancelled {
		t.Fatalf("old plan: %s", d.Run.Status)
	}
	g.wait(t, nr.ID, store.StatusWaitingApproval)
	if err := g.r.Approve(nr.ID, ApproveRequest{}); err != nil {
		t.Fatal(err)
	}
	g.wait(t, nr.ID, store.StatusSucceeded)
}

func TestConfigChangeAndAgeBlockApproval(t *testing.T) {
	g := newRig(t)
	os.WriteFile(filepath.Join(g.root, "terraform/envs/dev/main.tf"), []byte("# v1\n"), 0o644)
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	g.wait(t, run.ID, store.StatusWaitingApproval)
	if reasons, _ := g.r.Freshness(run.ID); len(reasons) != 0 {
		t.Fatalf("fresh plan reported stale: %v", reasons)
	}

	os.WriteFile(filepath.Join(g.root, "terraform/envs/dev/main.tf"), []byte("# v2\n"), 0o644)
	if reasons, _ := g.r.Freshness(run.ID); len(reasons) != 1 || !strings.Contains(reasons[0], "configuration changed") {
		t.Fatalf("%v", reasons)
	}
	if err := g.r.Approve(run.ID, ApproveRequest{}); !errors.Is(err, ErrStale) {
		t.Fatalf("%v", err)
	}
	os.WriteFile(filepath.Join(g.root, "terraform/envs/dev/main.tf"), []byte("# v1\n"), 0o644) // reverted: fine again
	// state files and terraform's working data are not "configuration"
	os.WriteFile(filepath.Join(g.root, "terraform/envs/dev/terraform.tfstate"), []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(g.root, "terraform/envs/dev/.terraform"), 0o755)
	os.WriteFile(filepath.Join(g.root, "terraform/envs/dev/.terraform/x"), []byte("y"), 0o644)
	if reasons, _ := g.r.Freshness(run.ID); len(reasons) != 0 {
		t.Fatalf("state/working files must not count as config changes: %v", reasons)
	}

	g.cfg.Terraform.PlanTTL = "1ns"
	time.Sleep(2 * time.Millisecond)
	if reasons, _ := g.r.Freshness(run.ID); len(reasons) != 1 || !strings.Contains(reasons[0], "older than") {
		t.Fatalf("%v", reasons)
	}
	if err := g.r.Approve(run.ID, ApproveRequest{}); !errors.Is(err, ErrStale) {
		t.Fatalf("%v", err)
	}
}

func TestVarFileChangeIsAConfigChange(t *testing.T) {
	g := newRig(t)
	vf := filepath.Join(g.root, "terraform/ws/env/prod.tfvars")
	os.MkdirAll(filepath.Dir(vf), 0o755)
	os.WriteFile(vf, []byte("n = 1\n"), 0o644)
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/ws", Env: "prod"})
	g.wait(t, run.ID, store.StatusWaitingApproval)
	os.WriteFile(vf, []byte("n = 2\n"), 0o644)
	if reasons, _ := g.r.Freshness(run.ID); len(reasons) != 1 {
		t.Fatalf("%v", reasons)
	}
}

func TestStateChangeWhileApplyIsQueuedIsCaughtUnderTheLock(t *testing.T) {
	g := newRig(t)
	sf := stateFile(t, 7, "abc")
	first, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	g.wait(t, first.ID, store.StatusWaitingApproval)

	// another job takes the root's lock
	t.Setenv("FAKE_PLAN_DELAY", "1")
	blocker, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	for i := 0; i < 200; i++ {
		if d, _ := g.r.Detail(blocker.ID); d.Run.Status == store.StatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := g.r.Approve(first.ID, ApproveRequest{}); err != nil {
		t.Fatal(err) // fresh at approval time
	}
	if d, _ := g.r.Detail(first.ID); d.Run.Status != store.StatusQueued {
		t.Fatalf("apply should wait for the lock, is %s", d.Run.Status)
	}
	writeState(t, sf, 8, "abc") // changes while the apply waits

	d := g.wait(t, first.ID, store.StatusFailed)
	if !strings.Contains(d.Summary.Error, "re-plan required") || !strings.Contains(d.Summary.Error, "serial 7 → 8") {
		t.Fatalf("%q", d.Summary.Error)
	}
	for _, l := range g.tfLog(t) {
		if strings.HasPrefix(l, "start apply") {
			t.Fatal("terraform apply ran against changed state")
		}
	}
}

func TestWorkspaceIsPinnedForStateAndApply(t *testing.T) {
	g := newRig(t)
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/ws", Env: "prod"})
	g.wait(t, run.ID, store.StatusWaitingApproval)
	g.r.Approve(run.ID, ApproveRequest{ConfirmText: "prod"})
	g.wait(t, run.ID, store.StatusSucceeded)
	for _, l := range g.tfLog(t) {
		if (strings.HasPrefix(l, "start state") || strings.HasPrefix(l, "start apply")) && !strings.HasSuffix(l, "ws=prod") {
			t.Fatalf("workspace not pinned: %s", l)
		}
	}
}

func TestDiffFileNeverHoldsSecrets(t *testing.T) {
	g := newRig(t)
	t.Setenv("FAKE_SHOW_JSON", abs(t, "../../testdata/terraform/show_sensitive.json"))
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	g.wait(t, run.ID, store.StatusWaitingApproval)
	b, err := os.ReadFile(filepath.Join(g.r.runDir(run.ID), "diff.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "hunter2") {
		t.Fatalf("diff.json holds a secret: %s", b)
	}
	if st, _ := os.Stat(filepath.Join(g.r.runDir(run.ID), "diff.json")); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}
