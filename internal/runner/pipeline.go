package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rapando/groundwork/internal/redact"
	"github.com/rapando/groundwork/internal/store"
	"github.com/rapando/groundwork/internal/terraform"
)

// lockKey: one active job per Terraform root. (The plan keys on root+workspace,
// but `workspace select` rewrites .terraform/ for the whole directory, so two
// workspaces of one root running together would race.)
func (j *job) lockKey() string {
	if j.target.isAnsible() {
		return "ans:" + j.target.Scope()
	}
	return "tf:" + j.target.Root
}

func (r *Runner) acquire(j *job) bool {
	r.mu.Lock()
	ch, ok := r.locks[j.lockKey()]
	if !ok {
		ch = make(chan struct{}, 1)
		r.locks[j.lockKey()] = ch
	}
	r.mu.Unlock()
	select {
	case ch <- struct{}{}: // blocked senders are served in arrival order
		return true
	case <-j.ctx.Done():
		return false
	}
}

func (r *Runner) release(j *job) {
	r.mu.Lock()
	ch := r.locks[j.lockKey()]
	r.mu.Unlock()
	<-ch
}

// begin opens the log and gives the job its own redactor.
func (r *Runner) begin(j *job) error {
	j.red = redact.New()
	j.red.AddEnv(os.Environ())
	lw, err := openLog(r.logPath(j.id), func(lines []LogLine) {
		r.Bus.Publish("log.line", map[string]any{"run": j.id, "lines": lines})
	})
	if err != nil {
		return err
	}
	j.log = lw
	return nil
}

func (r *Runner) finish(j *job) {
	if j.log != nil {
		j.log.close()
	}
	j.cancel()
	r.dropJob(j)
	r.publishRun(j.id)
}

// stage runs fn as a named, recorded stage.
func (r *Runner) stage(j *job, name string, fn func() error) error {
	now := time.Now()
	_ = r.Store.UpsertStage(store.RunStage{RunID: j.id, Name: name, Status: "running", StartedAt: &now})
	_ = r.Store.UpdateRun(j.id, store.RunUpdate{Stage: &name})
	r.publishRun(j.id)
	j.emit(name, "info", "── "+name+" ──", nil)

	err := fn()

	end := time.Now()
	st := store.RunStage{RunID: j.id, Name: name, Status: "succeeded", EndedAt: &end}
	switch {
	case errors.Is(err, errCancelled):
		st.Status = "cancelled"
	case err != nil:
		st.Status, st.Detail = "failed", err.Error()
	}
	_ = r.Store.UpsertStage(st)
	r.publishRun(j.id)
	return err
}

func (r *Runner) skipPending(j *job) {
	stages, _ := r.Store.ListStages(j.id)
	now := time.Now()
	for _, s := range stages {
		if s.Status == "pending" {
			_ = r.Store.UpsertStage(store.RunStage{RunID: j.id, Name: s.Name, Status: "skipped", EndedAt: &now})
		}
	}
}

func (r *Runner) saveSummary(id int64, mutate func(*Summary)) {
	run, err := r.Store.GetRun(id)
	if err != nil {
		return
	}
	s := readSummary(run)
	mutate(&s)
	_ = r.Store.UpdateRun(id, store.RunUpdate{Summary: s})
}

// end records the run's final state.
func (r *Runner) end(j *job, status string, exit *int, errMsg string) {
	now := time.Now()
	if errMsg != "" {
		r.saveSummary(j.id, func(s *Summary) { s.Error = errMsg })
	}
	r.skipPending(j)
	_ = r.Store.UpdateRun(j.id, store.RunUpdate{Status: status, Ended: &now, ExitCode: exit})
	j.emit("", map[string]string{store.StatusFailed: "error", store.StatusCancelled: "warn"}[status]+"", "run "+status, nil)
}

// failure turns a stage error into the run's final state.
func (r *Runner) failure(j *job, err error, exit int) {
	if errors.Is(err, errCancelled) {
		r.end(j, store.StatusCancelled, nil, "")
		return
	}
	msg := err.Error()
	if e := j.errText(); e != "" {
		msg += ": " + e
	}
	r.end(j, store.StatusFailed, &exit, msg)
}

func (r *Runner) tfStage(j *job, name string, argv []string, jsonMode bool) (execResult, error) {
	var res execResult
	err := r.stage(j, name, func() error {
		var e error
		res, e = r.execute(j, execOpts{stage: name, argv: argv, jsonMode: jsonMode})
		if e != nil {
			return e
		}
		if res.exit != 0 && !(name == "plan" && res.exit == 2) {
			return fmt.Errorf("terraform %s failed (exit %d)", name, res.exit)
		}
		return nil
	})
	return res, err
}

func (r *Runner) preStages(j *job) []string {
	names := []string{"init"}
	if j.target.Workspace != "" {
		names = append(names, "workspace")
	}
	return append(names, "validate", "plan", "summary", "approve", "apply")
}

func (r *Runner) runInit(j *job) {
	defer r.finish(j)
	if err := r.begin(j); err != nil {
		r.Log.Error("open log", "err", err)
		return
	}
	_ = r.Store.UpsertStage(store.RunStage{RunID: j.id, Name: "init", Status: "pending"})
	if !r.acquire(j) {
		r.end(j, store.StatusCancelled, nil, "")
		return
	}
	defer r.release(j)
	start := time.Now()
	_ = r.Store.UpdateRun(j.id, store.RunUpdate{Status: store.StatusRunning, Started: &start})
	r.publishRun(j.id)
	dir := r.abs(j.target.Root)
	res, err := r.tfStage(j, "init", terraform.InitArgs(j.target.Binary, dir, j.target.Upgrade), false)
	if err != nil {
		r.failure(j, err, res.exit)
		return
	}
	zero := 0
	r.end(j, store.StatusSucceeded, &zero, "")
}

// runUnlock: force-unlock, in the target's workspace. It takes the root's job
// lock, so it can't race a groundwork plan or apply on the same root.
func (r *Runner) runUnlock(j *job, argv []string) {
	defer r.finish(j)
	if err := r.begin(j); err != nil {
		r.Log.Error("open log", "err", err)
		return
	}
	_ = r.Store.UpsertStage(store.RunStage{RunID: j.id, Name: "unlock", Status: "pending"})
	if !r.acquire(j) {
		r.end(j, store.StatusCancelled, nil, "")
		return
	}
	defer r.release(j)
	start := time.Now()
	_ = r.Store.UpdateRun(j.id, store.RunUpdate{Status: store.StatusRunning, Started: &start})
	r.publishRun(j.id)
	var res execResult
	err := r.stage(j, "unlock", func() error {
		var e error
		res, e = r.execute(j, execOpts{stage: "unlock", argv: argv, env: workspaceEnv(j.target)})
		if e == nil && res.exit != 0 {
			e = fmt.Errorf("terraform force-unlock failed (exit %d)", res.exit)
		}
		return e
	})
	if err != nil {
		r.failure(j, err, res.exit)
		return
	}
	zero := 0
	r.end(j, store.StatusSucceeded, &zero, "")
}

// runPlanPhase: init → [workspace] → validate → plan → summary, then wait for approval.
func (r *Runner) runPlanPhase(j *job) {
	defer r.finish(j)
	if err := r.begin(j); err != nil {
		r.Log.Error("open log", "err", err)
		r.end(j, store.StatusFailed, nil, err.Error())
		return
	}
	for _, n := range r.preStages(j) {
		_ = r.Store.UpsertStage(store.RunStage{RunID: j.id, Name: n, Status: "pending"})
	}
	if !r.acquire(j) {
		r.end(j, store.StatusCancelled, nil, "")
		return
	}
	locked := true
	defer func() {
		if locked {
			r.release(j)
		}
	}()
	start := time.Now()
	_ = r.Store.UpdateRun(j.id, store.RunUpdate{Status: store.StatusRunning, Started: &start})
	r.publishRun(j.id)

	t := j.target
	dir := r.abs(t.Root)
	steps := []struct {
		name string
		argv []string
		json bool
	}{{"init", terraform.InitArgs(t.Binary, dir, t.Upgrade), false}}
	if t.Workspace != "" {
		steps = append(steps, struct {
			name string
			argv []string
			json bool
		}{"workspace", terraform.WorkspaceArgs(t.Binary, dir, t.Workspace), false})
	}
	steps = append(steps, struct {
		name string
		argv []string
		json bool
	}{"validate", terraform.ValidateArgs(t.Binary, dir), false})
	for _, s := range steps {
		if res, err := r.tfStage(j, s.name, s.argv, s.json); err != nil {
			r.failure(j, err, res.exit)
			return
		}
	}

	planFile := r.planPath(j.id)
	res, err := r.tfStage(j, "plan", terraform.PlanArgs(t.Binary, dir, planFile, r.absAll(t.VarFiles)), true)
	if err != nil {
		r.failure(j, err, res.exit)
		return
	}
	if res.exit == 0 { // nothing to do
		now := time.Now()
		r.saveSummary(j.id, func(s *Summary) { s.NoChanges = true; s.Plan = &terraform.PlanSummary{Changes: []terraform.Change{}} })
		_ = r.Store.UpsertStage(store.RunStage{RunID: j.id, Name: "approve", Status: "skipped", EndedAt: &now, Detail: "no changes"})
		_ = r.Store.UpsertStage(store.RunStage{RunID: j.id, Name: "apply", Status: "skipped", EndedAt: &now, Detail: "no changes"})
		r.removePlan(j.id)
		zero := 0
		r.end(j, store.StatusSucceeded, &zero, "")
		return
	}

	// summarise the saved plan; the raw `show -json` stays on disk, never in the log
	var show bytes.Buffer
	err = r.stage(j, "summary", func() error {
		e := error(nil)
		res, e = r.execute(j, execOpts{stage: "summary", argv: terraform.ShowPlanArgs(t.Binary, dir, planFile), capture: &show})
		if e != nil {
			return e
		}
		if res.exit != 0 {
			return fmt.Errorf("terraform show failed (exit %d)", res.exit)
		}
		return nil
	})
	if err != nil {
		r.failure(j, err, res.exit)
		return
	}
	sum, err := terraform.SummarizePlan(show.Bytes())
	if err != nil {
		r.failure(j, err, 1)
		return
	}
	// Keep only the masked diff on disk; the raw `show -json` can hold secrets.
	diffs, err := terraform.ComputeDiffs(show.Bytes())
	if err != nil {
		r.failure(j, err, 1)
		return
	}
	if b, err := json.Marshal(diffs); err == nil {
		_ = os.WriteFile(filepath.Join(r.runDir(j.id), "diff.json"), b, 0o600)
	}
	var extra []string
	if cfg := r.Cfg(); cfg != nil {
		extra = cfg.Terraform.Danger
	}
	danger := terraform.EvaluateDanger(sum, extra, terraform.ProtectedResources(r.Root))
	sha, err := fileSHA(planFile)
	if err != nil {
		r.failure(j, err, 1)
		return
	}
	state, err := r.readState(j.ctx, t)
	if err != nil {
		r.failure(j, fmt.Errorf("recording the state the plan was made against: %w", err), 1)
		return
	}
	fp, err := r.fingerprint(t)
	if err != nil {
		r.failure(j, err, 1)
		return
	}
	planned := time.Now().UTC()
	r.saveSummary(j.id, func(s *Summary) {
		s.Plan, s.PlanSHA256, s.Danger, s.State, s.Fingerprint, s.PlannedAt = sum, sha, danger, &state, fp, &planned
	})
	j.emit("summary", "info", fmt.Sprintf("Plan: %d to add, %d to change, %d to replace, %d to destroy. Waiting for approval.",
		sum.Create, sum.Update, sum.Replace, sum.Delete), nil)
	for _, d := range danger {
		j.emit("summary", "warn", "Danger: "+d.Message, nil)
	}

	// hand the lock back while a human decides; Approve re-acquires it for the apply
	now := time.Now()
	approve := "approve"
	_ = r.Store.UpsertStage(store.RunStage{RunID: j.id, Name: "approve", Status: "running", StartedAt: &now, Detail: "waiting for approval"})
	_ = r.Store.UpdateRun(j.id, store.RunUpdate{Status: store.StatusWaitingApproval, Stage: &approve})
}

// runApplyPhase applies the saved plan after Approve.
func (r *Runner) runApplyPhase(j *job) {
	defer r.finish(j)
	if err := r.begin(j); err != nil {
		r.end(j, store.StatusFailed, nil, err.Error())
		return
	}
	if !r.acquire(j) {
		r.end(j, store.StatusCancelled, nil, "")
		r.removePlan(j.id)
		return
	}
	defer r.release(j)
	_ = r.Store.UpdateRun(j.id, store.RunUpdate{Status: store.StatusRunning})
	r.publishRun(j.id)

	t := j.target
	var res execResult
	err := r.stage(j, "apply", func() error {
		// Authoritative freshness check: under the root lock, right before terraform runs.
		run, err := r.Store.GetRun(j.id)
		if err != nil {
			return err
		}
		j.emit("apply", "info", "Checking the plan is still current…", nil)
		reasons, _, err := r.staleness(j.ctx, t, readSummary(run), true)
		if err != nil {
			return fmt.Errorf("could not verify the plan is current: %w", err)
		}
		if len(reasons) > 0 {
			return &StaleError{Reasons: reasons}
		}
		var e error
		res, e = r.execute(j, execOpts{stage: "apply", argv: terraform.ApplyArgs(t.Binary, r.abs(t.Root), r.planPath(j.id)), jsonMode: true,
			env: workspaceEnv(t)})
		if e != nil {
			return e
		}
		if res.exit != 0 {
			return fmt.Errorf("terraform apply failed (exit %d)", res.exit)
		}
		return nil
	})
	r.removePlan(j.id) // the plan is consumed (or stale after a partial apply); re-plan to continue
	if err != nil {
		if errors.Is(err, errCancelled) && res.killed {
			// SIGKILL means terraform could not release the state lock
			_ = r.Store.RaiseIssue("tf-state-lock-possible", t.Root, map[string]any{"run": j.id, "env": t.Env})
		}
		r.failure(j, err, res.exit)
		return
	}
	if a := j.applySummary(); a != nil {
		r.saveSummary(j.id, func(s *Summary) { s.Apply = a })
	}
	zero := 0
	r.end(j, store.StatusSucceeded, &zero, "")
}

// workspaceEnv pins the workspace for commands that run after `workspace
// select`, so they can't be affected by another job selecting a different one.
func workspaceEnv(t Target) []string {
	if t.Workspace == "" {
		return nil
	}
	return []string{"TF_WORKSPACE=" + t.Workspace}
}
