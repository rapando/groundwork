package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
)

type rig struct {
	r    *Runner
	root string
	st   *store.Store
	bus  *events.Bus
	log  string
	cfg  *config.Config
}

func abs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"terraform/envs/dev", "terraform/envs/prod", "terraform/ws"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	cfg := &config.Config{Version: 1, Mode: "standalone", Terraform: config.Terraform{Binary: "terraform", Roots: []config.TFRoot{
		{Path: "terraform/envs/dev", Env: "dev"},
		{Path: "terraform/envs/prod", Env: "prod"}, // prod-like: approval required by default
		{Path: "terraform/ws", Envs: map[string]config.TFEnv{
			"dev":  {Workspace: "dev", VarFiles: []string{"env/dev.tfvars"}},
			"prod": {Workspace: "prod", VarFiles: []string{"env/prod.tfvars"}},
		}},
	}}}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bus := events.NewBus()
	r := New(root, st, bus, func() *config.Config { return cfg }, nil)
	r.KillGrace = 300 * time.Millisecond

	tfLog := filepath.Join(t.TempDir(), "tf.log")
	t.Setenv("PATH", abs(t, "../../testdata/fake-terraform")+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DATA", abs(t, "../../testdata/terraform"))
	t.Setenv("FAKE_TF_LOG", tfLog)
	t.Cleanup(func() { r.Shutdown(context.Background()) })
	return &rig{r, root, st, bus, tfLog, cfg}
}

func (g *rig) tfLog(t *testing.T) []string {
	b, _ := os.ReadFile(g.log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (g *rig) wait(t *testing.T, id int64, status ...string) *Detail {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		d, err := g.r.Detail(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range status {
			if d.Run.Status == s {
				return d
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	d, _ := g.r.Detail(id)
	t.Fatalf("run %d: wanted %v, still %s (stage %s)\nlog: %v", id, status, d.Run.Status, d.Run.Stage, g.dump(id))
	return nil
}

func (g *rig) dump(id int64) []string {
	p, _ := g.r.ReadLog(id, 0, 1000, "", "")
	var out []string
	for _, l := range p.Lines {
		out = append(out, l.Stage+": "+l.Text)
	}
	return out
}

func stageStatus(d *Detail) map[string]string {
	m := map[string]string{}
	for _, s := range d.Stages {
		m[s.Name] = s.Status
	}
	return m
}

func TestPlanWaitsForApprovalThenApplies(t *testing.T) {
	g := newRig(t)
	sub, cancel := g.bus.Subscribe()
	defer cancel()

	run, err := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/prod", Env: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	d := g.wait(t, run.ID, store.StatusWaitingApproval)
	st := stageStatus(d)
	for _, n := range []string{"init", "validate", "plan", "summary"} {
		if st[n] != "succeeded" {
			t.Errorf("stage %s: %s", n, st[n])
		}
	}
	order := []string{}
	for _, s := range d.Stages {
		order = append(order, s.Name)
	}
	if strings.Join(order, ",") != "init,validate,plan,summary,approve,apply" {
		t.Fatalf("stages must be listed in pipeline order: %v", order)
	}
	if st["approve"] != "running" || st["apply"] != "pending" {
		t.Fatalf("stages while waiting: %v", st)
	}
	if d.Summary.Plan == nil || d.Summary.Plan.Create != 3 || d.Summary.PlanSHA256 == "" || !d.Target.ApprovalRequired {
		t.Fatalf("%+v", d.Summary)
	}
	if _, err := os.Stat(g.r.planPath(run.ID)); err != nil {
		t.Fatalf("plan file must be kept for the apply: %v", err)
	}
	// nothing applied yet
	for _, l := range g.tfLog(t) {
		if strings.HasPrefix(l, "start apply") {
			t.Fatal("apply ran without approval")
		}
	}

	// prod requires typing the environment name
	if err := g.r.Approve(run.ID, ApproveRequest{ConfirmText: ""}); err == nil || !strings.Contains(err.Error(), "type \"prod\"") {
		t.Fatalf("empty confirmation: %v", err)
	}
	if err := g.r.Approve(run.ID, ApproveRequest{ConfirmText: "dev"}); err == nil {
		t.Fatal("wrong confirmation accepted")
	}
	if d, _ := g.r.Detail(run.ID); d.Run.Status != store.StatusWaitingApproval {
		t.Fatal("a refused approval must leave the run waiting")
	}
	if err := g.r.Approve(run.ID, ApproveRequest{ConfirmText: "prod"}); err != nil {
		t.Fatal(err)
	}
	if err := g.r.Approve(run.ID, ApproveRequest{ConfirmText: "prod"}); err == nil {
		t.Fatal("double approval")
	}
	d = g.wait(t, run.ID, store.StatusSucceeded)
	if st := stageStatus(d); st["apply"] != "succeeded" || st["approve"] != "succeeded" {
		t.Fatalf("%v", st)
	}
	if d.Approval == nil || d.Approval.ConfirmText != "prod" || d.Approval.PlanSHA256 != d.Summary.PlanSHA256 || d.Approval.ApprovedBy == "" {
		t.Fatalf("approval record: %+v", d.Approval)
	}
	if d.Summary.Apply == nil || d.Summary.Apply.Added != 3 || len(d.Summary.Apply.Outputs) != 1 {
		t.Fatalf("apply summary %+v", d.Summary.Apply)
	}
	// the exact saved plan was applied, then deleted
	applied := false
	for _, l := range g.tfLog(t) {
		if strings.HasPrefix(l, "applying PLANFILE") {
			applied = true
		}
	}
	if !applied {
		t.Fatal("apply must receive the saved plan file")
	}
	if _, err := os.Stat(g.r.planPath(run.ID)); !os.IsNotExist(err) {
		t.Fatal("a consumed plan must be removed")
	}
	// per-resource progress is available
	res, _ := g.r.Resources(run.ID)
	if len(res) != 3 {
		t.Fatalf("%+v", res)
	}
	for _, x := range res {
		if x.State != "done" || x.Action != "create" {
			t.Fatalf("%+v", x)
		}
	}
	// events were published
	var upd, lines int
	for len(sub) > 0 {
		switch (<-sub).Type {
		case "run.updated":
			upd++
		case "log.line":
			lines++
		}
	}
	if upd < 5 || lines == 0 {
		t.Fatalf("run.updated=%d log.line=%d", upd, lines)
	}
}

func TestNonProdNeedsNoTypedConfirmation(t *testing.T) {
	g := newRig(t)
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	if d := g.wait(t, run.ID, store.StatusWaitingApproval); d.Target.ApprovalRequired {
		t.Fatal("dev should not require typed approval")
	}
	if err := g.r.Approve(run.ID, ApproveRequest{ConfirmText: ""}); err != nil {
		t.Fatal(err)
	}
	g.wait(t, run.ID, store.StatusSucceeded)
}

func TestNoChangesSkipsApproval(t *testing.T) {
	g := newRig(t)
	t.Setenv("FAKE_PLAN_EXIT", "0")
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	d := g.wait(t, run.ID, store.StatusSucceeded)
	st := stageStatus(d)
	if !d.Summary.NoChanges || st["approve"] != "skipped" || st["apply"] != "skipped" {
		t.Fatalf("%+v %v", d.Summary, st)
	}
	for _, l := range g.tfLog(t) {
		if strings.HasPrefix(l, "start show") || strings.HasPrefix(l, "start apply") {
			t.Fatalf("unexpected %s", l)
		}
	}
}

func TestFailureIsRedactedAndSkipsLaterStages(t *testing.T) {
	g := newRig(t)
	t.Setenv("FAKE_FAIL_STAGE", "validate")
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	d := g.wait(t, run.ID, store.StatusFailed)
	st := stageStatus(d)
	if st["init"] != "succeeded" || st["validate"] != "failed" || st["plan"] != "skipped" || st["apply"] != "skipped" {
		t.Fatalf("%v", st)
	}
	if !strings.Contains(d.Summary.Error, "terraform validate failed (exit 1)") || !strings.Contains(d.Summary.Error, "validate failed: boom") {
		t.Fatalf("error should carry the root cause: %q", d.Summary.Error)
	}
	// the secret in terraform's stderr never reaches the log or the stored summary
	raw, _ := os.ReadFile(g.r.logPath(run.ID))
	if strings.Contains(string(raw), "AKIAIOSFODNN7EXAMPLE") || strings.Contains(d.Summary.Error, "AKIA") {
		t.Fatalf("secret leaked:\n%s\n%s", raw, d.Summary.Error)
	}
	if !strings.Contains(string(raw), "••••") {
		t.Fatal("expected a redaction marker in the log")
	}
	if d.Run.ExitCode == nil || *d.Run.ExitCode != 1 {
		t.Fatalf("exit code %v", d.Run.ExitCode)
	}
}

func TestEnvSecretsAreMaskedEverywhere(t *testing.T) {
	g := newRig(t)
	t.Setenv("MY_DB_PASSWORD", "hunter2-hunter2")
	t.Setenv("FAKE_FAIL_STAGE", "init")
	// make the fake echo the password into the output by failing with it in the message
	script := filepath.Join(t.TempDir(), "bin")
	os.MkdirAll(script, 0o755)
	os.WriteFile(filepath.Join(script, "terraform"), []byte("#!/bin/sh\necho \"connecting with $MY_DB_PASSWORD\"\necho \"Error: login failed for $MY_DB_PASSWORD\" >&2\nexit 1\n"), 0o755)
	t.Setenv("PATH", script+string(os.PathListSeparator)+os.Getenv("PATH"))
	run, _ := g.r.Submit(SubmitRequest{Kind: KindInit, Root: "terraform/envs/dev", Env: "dev"})
	d := g.wait(t, run.ID, store.StatusFailed)
	raw, _ := os.ReadFile(g.r.logPath(run.ID))
	if strings.Contains(string(raw), "hunter2") || strings.Contains(d.Summary.Error, "hunter2") {
		t.Fatalf("env secret leaked: %s | %s", raw, d.Summary.Error)
	}
}

func TestWorkspaceRootSelectsWorkspaceAndPassesAbsoluteVarFiles(t *testing.T) {
	g := newRig(t)
	run, err := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/ws", Env: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	d := g.wait(t, run.ID, store.StatusWaitingApproval)
	if d.Target.Workspace != "prod" || stageStatus(d)["workspace"] != "succeeded" {
		t.Fatalf("%+v %v", d.Target, stageStatus(d))
	}
	var order []string
	var planArgv string
	for _, l := range g.tfLog(t) {
		if strings.HasPrefix(l, "start ") {
			order = append(order, strings.Fields(l)[1])
		}
		if strings.HasPrefix(l, "argv ") && strings.Contains(l, " plan ") {
			planArgv = l
		}
	}
	if strings.Join(order, ",") != "init,workspace,validate,plan,show,state" {
		t.Fatalf("order %v", order)
	}
	want := "-var-file=" + filepath.Join(g.root, "terraform/ws/env/prod.tfvars")
	if !strings.Contains(planArgv, want) || !strings.Contains(planArgv, "-out="+g.r.planPath(run.ID)) || !strings.Contains(planArgv, "-chdir="+filepath.Join(g.root, "terraform/ws")) {
		t.Fatalf("plan argv: %s", planArgv)
	}
}

func TestSameRootJobsNeverOverlapDifferentRootsDo(t *testing.T) {
	g := newRig(t)
	t.Setenv("FAKE_PLAN_DELAY", "0.4")
	a, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	b, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	c, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/prod", Env: "prod"})
	for _, id := range []int64{a.ID, b.ID, c.ID} {
		g.wait(t, id, store.StatusWaitingApproval)
	}
	// Judged from the command log, not status snapshots: either dev job may take
	// the root first, and a fixed sleep overruns a plan on a loaded machine.
	// Between "start plan <dev>" and its "end", no other dev command starts,
	// and prod starts before dev's first plan ends (it didn't wait for dev).
	open := map[string]int{}
	prodStarted, devPlanEnded := false, false
	for _, l := range g.tfLog(t) {
		f := strings.Fields(l)
		if len(f) < 3 || (f[0] != "start" && f[0] != "end") {
			continue
		}
		if f[0] == "start" {
			open[f[2]]++
			if open[f[2]] > 1 {
				t.Fatalf("two commands ran at once in %s", f[2])
			}
			if strings.HasSuffix(f[2], "envs/prod") && !devPlanEnded {
				prodStarted = true
			}
		} else {
			open[f[2]]--
			if f[1] == "plan" && strings.HasSuffix(f[2], "envs/dev") {
				devPlanEnded = true
			}
		}
	}
	if !prodStarted {
		t.Fatalf("a different root must not wait\nlog: %v", g.tfLog(t))
	}
}

func TestCancelDuringApplyReleasesLockViaSIGINT(t *testing.T) {
	g := newRig(t)
	t.Setenv("FAKE_APPLY_DELAY", "5")
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	g.wait(t, run.ID, store.StatusWaitingApproval)
	g.r.Approve(run.ID, ApproveRequest{ConfirmText: ""})
	for i := 0; i < 200; i++ { // wait until terraform is actually applying
		if d, _ := g.r.Detail(run.ID); stageStatus(d)["apply"] == "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	start := time.Now()
	if err := g.r.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	d := g.wait(t, run.ID, store.StatusCancelled)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("cancel took %s; SIGINT should stop it promptly", time.Since(start))
	}
	if stageStatus(d)["apply"] != "cancelled" {
		t.Fatalf("%v", stageStatus(d))
	}
	// terraform exited on SIGINT (released its lock): no stale-lock issue
	var n int
	g.st.DB.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&n)
	if n != 0 {
		t.Fatalf("graceful cancel raised %d issue(s)", n)
	}
	// the root's lock is free: the next job starts immediately
	t.Setenv("FAKE_APPLY_DELAY", "0")
	next, _ := g.r.Submit(SubmitRequest{Kind: KindInit, Root: "terraform/envs/dev", Env: "dev"})
	g.wait(t, next.ID, store.StatusSucceeded)
}

func TestCancelEscalatesToSIGKILLAndRaisesStaleLockIssue(t *testing.T) {
	g := newRig(t)
	t.Setenv("FAKE_APPLY_DELAY", "5")
	t.Setenv("FAKE_IGNORE_INT", "1")
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	g.wait(t, run.ID, store.StatusWaitingApproval)
	g.r.Approve(run.ID, ApproveRequest{ConfirmText: ""})
	for i := 0; i < 200; i++ {
		if d, _ := g.r.Detail(run.ID); stageStatus(d)["apply"] == "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	g.r.Cancel(run.ID)
	g.wait(t, run.ID, store.StatusCancelled)
	var rule, target string
	if err := g.st.DB.QueryRow(`SELECT rule_id, target FROM issues`).Scan(&rule, &target); err != nil {
		t.Fatalf("a SIGKILLed apply should raise an issue: %v", err)
	}
	if rule != "tf-state-lock-possible" || target != "terraform/envs/dev" {
		t.Fatalf("%s %s", rule, target)
	}
}

func TestCancelQueuedJobNeverRunsTerraform(t *testing.T) {
	g := newRig(t)
	t.Setenv("FAKE_PLAN_DELAY", "0.5")
	a, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	b, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	time.Sleep(100 * time.Millisecond)
	if err := g.r.Cancel(b.ID); err != nil {
		t.Fatal(err)
	}
	d := g.wait(t, b.ID, store.StatusCancelled)
	if st := stageStatus(d); st["init"] != "skipped" {
		t.Fatalf("%v", st)
	}
	g.wait(t, a.ID, store.StatusWaitingApproval)
	inits := 0
	for _, l := range g.tfLog(t) {
		if strings.HasPrefix(l, "start init") {
			inits++
		}
	}
	if inits != 1 {
		t.Fatalf("the cancelled job ran terraform (%d inits)", inits)
	}
}

func TestCancelWaitingPlanDiscardsIt(t *testing.T) {
	g := newRig(t)
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	g.wait(t, run.ID, store.StatusWaitingApproval)
	if err := g.r.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	d, _ := g.r.Detail(run.ID)
	if d.Run.Status != store.StatusCancelled || stageStatus(d)["approve"] != "cancelled" {
		t.Fatalf("%+v %v", d.Run.Status, stageStatus(d))
	}
	if _, err := os.Stat(g.r.planPath(run.ID)); !os.IsNotExist(err) {
		t.Fatal("discarded plan must be deleted")
	}
	if err := g.r.Approve(run.ID, ApproveRequest{ConfirmText: ""}); err == nil {
		t.Fatal("cannot approve a cancelled plan")
	}
	if err := g.r.Cancel(run.ID); err != ErrNotCancelable {
		t.Fatalf("finished run: %v", err)
	}
}

func TestTamperedPlanFileCannotBeApproved(t *testing.T) {
	g := newRig(t)
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	g.wait(t, run.ID, store.StatusWaitingApproval)
	os.WriteFile(g.r.planPath(run.ID), []byte("something else"), 0o600)
	if err := g.r.Approve(run.ID, ApproveRequest{ConfirmText: ""}); err != ErrPlanChanged {
		t.Fatalf("%v", err)
	}
	for _, l := range g.tfLog(t) {
		if strings.HasPrefix(l, "start apply") {
			t.Fatal("applied a modified plan")
		}
	}
}

func TestApprovalSurvivesRestart(t *testing.T) {
	g := newRig(t)
	run, _ := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/prod", Env: "prod"})
	g.wait(t, run.ID, store.StatusWaitingApproval)
	// a stale in-flight run from the "previous process"
	stale, _ := g.st.CreateRun(KindPlan, Target{Root: "terraform/envs/dev", Env: "dev"}, nil, "")
	g.st.UpdateRun(stale.ID, store.RunUpdate{Status: store.StatusRunning})
	stage := "apply"
	g.st.UpdateRun(stale.ID, store.RunUpdate{Stage: &stage})

	// "restart": a brand-new runner over the same store and directory
	r2 := New(g.root, g.st, g.bus, func() *config.Config { return g.cfg }, nil)
	r2.KillGrace = g.r.KillGrace
	if err := r2.Recover(500, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if d, _ := r2.Detail(stale.ID); d.Run.Status != store.StatusFailed || !strings.Contains(d.Summary.Error, "interrupted") {
		t.Fatalf("%+v %+v", d.Run.Status, d.Summary)
	}
	var rule, target string
	if err := g.st.DB.QueryRow(`SELECT rule_id, target FROM issues`).Scan(&rule, &target); err != nil || rule != "tf-state-lock-possible" || target != "terraform/envs/dev" {
		t.Fatalf("an interrupted apply must raise a stale-lock issue: %v %s %s", err, rule, target)
	}
	if d, _ := r2.Detail(run.ID); d.Run.Status != store.StatusWaitingApproval {
		t.Fatal("waiting plans survive a restart")
	}
	if err := r2.Approve(run.ID, ApproveRequest{ConfirmText: "prod"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if d, _ := r2.Detail(run.ID); d.Run.Status == store.StatusSucceeded {
			// the apply continued the same log
			p, _ := r2.ReadLog(run.ID, 0, 5000, "", "")
			seenPlan, seenApply := false, false
			for i, l := range p.Lines {
				if i > 0 && l.N != p.Lines[i-1].N+1 {
					t.Fatalf("log numbering has a gap at %d", l.N)
				}
				seenPlan = seenPlan || l.Stage == "plan"
				seenApply = seenApply || l.Stage == "apply"
			}
			if !seenPlan || !seenApply {
				t.Fatal("log should hold both phases")
			}
			r2.Wait()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("approval after restart did not complete")
}

func TestMissingTerraformIsAClearFailure(t *testing.T) {
	g := newRig(t)
	t.Setenv("PATH", t.TempDir())
	run, _ := g.r.Submit(SubmitRequest{Kind: KindInit, Root: "terraform/envs/dev", Env: "dev"})
	d := g.wait(t, run.ID, store.StatusFailed)
	if !strings.Contains(d.Summary.Error, "terraform not found on PATH") {
		t.Fatalf("%q", d.Summary.Error)
	}
}

func TestResolveTarget(t *testing.T) {
	g := newRig(t)
	for _, c := range []struct{ root, env string }{
		{"terraform/nope", "dev"}, {"terraform/envs/dev", "prod"}, {"terraform/ws", "staging"}, {"../../etc", "dev"},
		{"terraform/envs/dev/../../../x", "dev"},
	} {
		if _, err := ResolveTarget(g.cfg, c.root, c.env); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if _, err := ResolveTarget(nil, "a", "b"); err == nil {
		t.Error("nil config accepted")
	}
	t1, err := ResolveTarget(g.cfg, "terraform/envs/dev/", "") // empty env = the root's own env
	if err != nil || t1.Env != "dev" || t1.ApprovalRequired {
		t.Fatalf("%+v %v", t1, err)
	}
	t2, _ := ResolveTarget(g.cfg, "terraform/ws", "prod")
	if t2.Workspace != "prod" || !t2.ApprovalRequired || len(t2.VarFiles) != 1 || t2.VarFiles[0] != "terraform/ws/env/prod.tfvars" {
		t.Fatalf("%+v", t2)
	}
	if _, err := g.r.Submit(SubmitRequest{Kind: "tf.destroy", Root: "terraform/envs/dev", Env: "dev"}); err == nil {
		t.Fatal("unknown kinds must be refused")
	}
}

func TestLogReadFilteringAndPaging(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "log.ndjson")
	w, _ := openLog(p, nil)
	w.add("plan", "info", "Creating vpc", nil)
	w.add("plan", "warn", "deprecated attribute", nil)
	w.add("plan", "error", "Error: boom", nil)
	w.add("apply", "info", "CREATING subnet", nil)
	w.close()

	all, _ := ReadLog(p, 0, 100, "", "")
	if len(all.Lines) != 4 || all.Total != 4 || all.Next != 4 {
		t.Fatalf("%+v", all)
	}
	if got, _ := ReadLog(p, 0, 100, "warn", ""); len(got.Lines) != 2 {
		t.Fatalf("level filter: %d", len(got.Lines))
	}
	if got, _ := ReadLog(p, 0, 100, "", "creating"); len(got.Lines) != 2 || got.Lines[1].Text != "CREATING subnet" {
		t.Fatalf("search should be case-insensitive: %+v", got.Lines)
	}
	page1, _ := ReadLog(p, 0, 2, "", "")
	page2, _ := ReadLog(p, page1.Next, 2, "", "")
	if len(page1.Lines) != 2 || len(page2.Lines) != 2 || page2.Lines[0].N != 2 {
		t.Fatalf("paging: %+v %+v", page1, page2)
	}
	if got, _ := ReadLog(filepath.Join(dir, "missing"), 0, 10, "", ""); len(got.Lines) != 0 {
		t.Fatal("missing log is empty, not an error")
	}
}

func TestLogCapKeepsHeadAndTail(t *testing.T) {
	oldHead, oldTail := logHeadCap, logTailKeep
	logHeadCap, logTailKeep = 600, 5
	defer func() { logHeadCap, logTailKeep = oldHead, oldTail }()
	p := filepath.Join(t.TempDir(), "log.ndjson")
	var live int
	w, _ := openLog(p, func(l []LogLine) { live += len(l) })
	for i := 0; i < 100; i++ {
		w.add("apply", "info", "line "+itoa(i), nil)
	}
	w.close()
	if live != 100 {
		t.Fatalf("live viewers should see every line, saw %d", live)
	}
	page, _ := ReadLog(p, 0, 1000, "", "")
	first, last := page.Lines[0].Text, page.Lines[len(page.Lines)-1].Text
	var marker bool
	for _, l := range page.Lines {
		marker = marker || strings.Contains(l.Text, "lines omitted")
	}
	if first != "line 0" || last != "line 99" || !marker || len(page.Lines) > 40 {
		t.Fatalf("head=%q tail=%q marker=%v n=%d", first, last, marker, len(page.Lines))
	}
}

func TestConcurrentStreamsDoNotRaceOnJobState(t *testing.T) {
	// stdout (json events) and stderr (errors) are read by different goroutines
	g := newRig(t)
	script := filepath.Join(t.TempDir(), "bin")
	os.MkdirAll(script, 0o755)
	body := "#!/bin/sh\ni=0\nwhile [ $i -lt 300 ]; do\n echo '{\"@level\":\"info\",\"@message\":\"Apply complete! Resources: 1 added, 0 changed, 0 destroyed.\",\"type\":\"change_summary\",\"changes\":{\"add\":1,\"change\":0,\"remove\":0,\"operation\":\"apply\"}}'\n echo \"Error: noise $i\" >&2\n i=$((i+1))\ndone\nexit 1\n"
	os.WriteFile(filepath.Join(script, "terraform"), []byte(body), 0o755)
	t.Setenv("PATH", script+string(os.PathListSeparator)+os.Getenv("PATH"))
	run, _ := g.r.Submit(SubmitRequest{Kind: KindInit, Root: "terraform/envs/dev", Env: "dev"})
	d := g.wait(t, run.ID, store.StatusFailed)
	if !strings.Contains(d.Summary.Error, "noise 0") { // the FIRST error is the root cause
		t.Fatalf("%q", d.Summary.Error)
	}
}
