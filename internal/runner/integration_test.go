//go:build integration

package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
)

// Real terraform, built-in terraform_data resource only: no cloud, no downloads.
func realRig(t *testing.T, tf string) (*Runner, *store.Store, string) {
	t.Helper()
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform not installed")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "terraform/envs/dev")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "main.tf"), []byte(tf), 0o644)
	cfg := &config.Config{Version: 1, Mode: "standalone", Terraform: config.Terraform{Binary: "terraform",
		Roots: []config.TFRoot{{Path: "terraform/envs/dev", Env: "dev"}}}}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := New(root, st, events.NewBus(), func() *config.Config { return cfg }, nil)
	t.Cleanup(func() { r.Shutdown(context.Background()) })
	return r, st, root
}

func waitStatus(t *testing.T, r *Runner, id int64, want string, within time.Duration) *Detail {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		d, _ := r.Detail(id)
		if d.Run.Status == want {
			return d
		}
		if d.Run.Status == store.StatusFailed && want != store.StatusFailed {
			p, _ := r.ReadLog(id, 0, 200, "", "")
			var lines []string
			for _, l := range p.Lines {
				lines = append(lines, l.Stage+": "+l.Text)
			}
			t.Fatalf("run failed while waiting for %s: %s\n%s", want, d.Summary.Error, strings.Join(lines, "\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	d, _ := r.Detail(id)
	t.Fatalf("wanted %s within %s, run is %s/%s", want, within, d.Run.Status, d.Run.Stage)
	return nil
}

func TestRealTerraformPlanApproveApplyReplan(t *testing.T) {
	r, _, root := realRig(t, `
terraform {
  backend "local" {}
}

resource "terraform_data" "a" {
  count = 3
  input = "x"
}

output "n" {
  value = length(terraform_data.a)
}
`)
	run, err := r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	d := waitStatus(t, r, run.ID, store.StatusWaitingApproval, 60*time.Second)
	if d.Summary.Plan.Create != 3 {
		t.Fatalf("%+v", d.Summary.Plan)
	}
	if _, err := os.Stat(filepath.Join(root, "terraform/envs/dev/terraform.tfstate")); err == nil {
		t.Fatal("planning must not create state")
	}

	if err := r.Approve(run.ID, ApproveRequest{ConfirmText: ""}); err != nil {
		t.Fatal(err)
	}
	d = waitStatus(t, r, run.ID, store.StatusSucceeded, 60*time.Second)
	if d.Summary.Apply == nil || d.Summary.Apply.Added != 3 || len(d.Summary.Apply.Outputs) != 1 || d.Summary.Apply.Outputs[0] != "n" {
		t.Fatalf("%+v", d.Summary.Apply)
	}
	res, _ := r.Resources(run.ID)
	if len(res) != 3 {
		t.Fatalf("%+v", res)
	}
	for _, x := range res {
		if x.State != "done" || !strings.HasPrefix(x.Address, "terraform_data.a[") {
			t.Fatalf("%+v", x)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "terraform/envs/dev/terraform.tfstate")); err != nil {
		t.Fatalf("apply should have written state: %v", err)
	}

	// converged: the next plan finds nothing and never asks for approval
	again, _ := r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	d = waitStatus(t, r, again.ID, store.StatusSucceeded, 60*time.Second)
	if !d.Summary.NoChanges {
		t.Fatalf("%+v", d.Summary)
	}
}

func TestRealTerraformCancelMidApplyReleasesTheStateLock(t *testing.T) {
	r, st, root := realRig(t, `
terraform {
  backend "local" {}
}

resource "terraform_data" "slow" {
  provisioner "local-exec" {
    command = "sleep 30"
  }
}
`)
	run, _ := r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	waitStatus(t, r, run.ID, store.StatusWaitingApproval, 60*time.Second)
	if err := r.Approve(run.ID, ApproveRequest{ConfirmText: ""}); err != nil {
		t.Fatal(err)
	}
	// wait until the provisioner is really running
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		p, _ := r.ReadLog(run.ID, 0, 500, "", "Executing")
		if len(p.Lines) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	lock := filepath.Join(root, "terraform/envs/dev/.terraform.tfstate.lock.info")
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("expected terraform to hold the state lock while applying: %v", err)
	}

	start := time.Now()
	if err := r.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, r, run.ID, store.StatusCancelled, 20*time.Second)
	t.Logf("cancelled in %s", time.Since(start))
	if time.Since(start) > 15*time.Second {
		t.Fatal("cancel should not need the SIGKILL escalation")
	}
	if _, err := os.Stat(lock); err == nil {
		t.Fatal("state lock still held after a graceful cancel")
	}
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&n)
	if n != 0 {
		t.Fatalf("graceful cancel must not raise a stale-lock issue (%d)", n)
	}

	// and the root is immediately usable again: a fresh plan gets the lock
	next, _ := r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	d := waitStatus(t, r, next.ID, store.StatusWaitingApproval, 60*time.Second)
	if d.Summary.Plan == nil {
		t.Fatal("no plan")
	}
}

func TestRealTerraformFailedApplyIsReportedWithItsCause(t *testing.T) {
	r, _, _ := realRig(t, `
terraform {
  backend "local" {}
}

resource "terraform_data" "boom" {
  provisioner "local-exec" {
    command = "echo about-to-fail; exit 3"
  }
}
`)
	run, _ := r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	waitStatus(t, r, run.ID, store.StatusWaitingApproval, 60*time.Second)
	r.Approve(run.ID, ApproveRequest{ConfirmText: ""})
	d := waitStatus(t, r, run.ID, store.StatusFailed, 60*time.Second)
	if !strings.Contains(d.Summary.Error, "terraform apply failed") || !strings.Contains(d.Summary.Error, "local-exec provisioner error") {
		t.Fatalf("%q", d.Summary.Error)
	}
	res, _ := r.Resources(run.ID)
	if len(res) != 1 || res[0].State != "failed" || res[0].Address != "terraform_data.boom" {
		t.Fatalf("%+v", res)
	}
}

func TestRealTerraformStateChangeInvalidatesApproval(t *testing.T) {
	r, _, root := realRig(t, `
terraform {
  backend "local" {}
}

variable "pw" {
  type      = string
  sensitive = true
  default   = "s3cret-from-default"
}

resource "terraform_data" "a" {
  input = var.pw
}

resource "terraform_data" "b" {
  input = "v1"
}
`)
	// converge first, so later plans have a prior state with a serial
	first, _ := r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	waitStatus(t, r, first.ID, store.StatusWaitingApproval, 60*time.Second)
	r.Approve(first.ID, ApproveRequest{})
	waitStatus(t, r, first.ID, store.StatusSucceeded, 60*time.Second)

	os.WriteFile(filepath.Join(root, "terraform/envs/dev/terraform.tfvars"), []byte("pw = \"n3w-s3cret\"\n"), 0o644)
	run, _ := r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	d := waitStatus(t, r, run.ID, store.StatusWaitingApproval, 60*time.Second)
	if d.Summary.State == nil || !d.Summary.State.Exists || d.Summary.State.Serial == 0 {
		t.Fatalf("plan should record the state serial: %+v", d.Summary.State)
	}
	diff, _ := os.ReadFile(filepath.Join(r.runDir(run.ID), "diff.json"))
	if strings.Contains(string(diff), "s3cret") {
		t.Fatalf("sensitive value in diff.json: %s", diff)
	}

	// someone changes the real state behind groundwork's back (config untouched)
	cmd := exec.Command("terraform", "apply", "-auto-approve", "-input=false", "-replace=terraform_data.b")
	cmd.Dir = filepath.Join(root, "terraform/envs/dev")
	cmd.Env = append(os.Environ(), "TF_DATA_DIR=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("out-of-band apply: %v\n%s", err, out)
	}
	err := r.Approve(run.ID, ApproveRequest{})
	if !errors.Is(err, ErrStale) || !strings.Contains(err.Error(), "state changed since the plan") {
		t.Fatalf("approval after an out-of-band state change must be refused, got %v", err)
	}
	if d, _ := r.Detail(run.ID); d.Run.Status != store.StatusWaitingApproval {
		t.Fatal("still waiting (re-plan to continue)")
	}
	t.Logf("refused: %v", err)
}
