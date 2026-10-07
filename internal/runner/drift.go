package runner

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rapando/groundwork/internal/store"
	"github.com/rapando/groundwork/internal/terraform"
)

// runDrift: init → [workspace] → refresh-only plan → record drift. Read-only:
// it takes the root lock (terraform reads state) but needs no approval, and
// the refresh-only plan is never applied.
func (r *Runner) runDrift(j *job) {
	defer r.finish(j)
	if err := r.begin(j); err != nil {
		r.end(j, store.StatusFailed, nil, err.Error())
		return
	}
	stages := []string{"init"}
	if j.target.Workspace != "" {
		stages = append(stages, "workspace")
	}
	stages = append(stages, "refresh", "summary")
	for _, n := range stages {
		_ = r.Store.UpsertStage(store.RunStage{RunID: j.id, Name: n, Status: "pending"})
	}
	if !r.acquire(j) {
		r.end(j, store.StatusCancelled, nil, "")
		return
	}
	defer r.release(j)
	start := time.Now()
	_ = r.Store.UpdateRun(j.id, store.RunUpdate{Status: store.StatusRunning, Started: &start})
	r.publishRun(j.id)

	t := j.target
	dir := r.abs(t.Root)
	if res, err := r.tfStage(j, "init", terraform.InitArgs(t.Binary, dir, false), false); err != nil {
		r.failure(j, err, res.exit)
		return
	}
	if t.Workspace != "" {
		if res, err := r.tfStage(j, "workspace", terraform.WorkspaceArgs(t.Binary, dir, t.Workspace), false); err != nil {
			r.failure(j, err, res.exit)
			return
		}
	}
	planFile := filepath.Join(r.runDir(j.id), "drift.tfplan")
	defer os.Remove(planFile) // a refresh-only plan is evidence, not something to apply
	var res execResult
	err := r.stage(j, "refresh", func() error {
		var e error
		res, e = r.execute(j, execOpts{stage: "refresh", argv: terraform.DriftArgs(t.Binary, dir, planFile, r.absAll(t.VarFiles)), jsonMode: true, env: workspaceEnv(t)})
		if e != nil {
			return e
		}
		if res.exit != 0 && res.exit != 2 {
			return fmt.Errorf("terraform plan -refresh-only failed (exit %d)", res.exit)
		}
		return nil
	})
	if err != nil {
		r.failure(j, err, res.exit)
		return
	}
	var rows []store.DriftRow
	if res.exit == 2 {
		var show bytes.Buffer
		err = r.stage(j, "summary", func() error {
			sr, e := r.execute(j, execOpts{stage: "summary", argv: terraform.ShowPlanArgs(t.Binary, dir, planFile), capture: &show, env: workspaceEnv(t)})
			if e != nil {
				return e
			}
			if sr.exit != 0 {
				return fmt.Errorf("terraform show failed (exit %d)", sr.exit)
			}
			return nil
		})
		if err != nil {
			r.failure(j, err, 1)
			return
		}
		drift, err := terraform.ComputeDrift(show.Bytes())
		if err != nil {
			r.failure(j, err, 1)
			return
		}
		for addr, d := range drift {
			if d.Action == "delete" {
				rows = append(rows, store.DriftRow{Address: addr, Action: "delete", Code: "exists in state", Actual: "deleted outside Terraform"})
				continue
			}
			for _, a := range d.Changed {
				rows = append(rows, store.DriftRow{Address: addr, Action: d.Action, AttrPath: a.Path, Code: orDash(a.Before), Actual: orDash(a.After)})
			}
		}
	} else {
		now := time.Now()
		_ = r.Store.UpsertStage(store.RunStage{RunID: j.id, Name: "summary", Status: "skipped", EndedAt: &now, Detail: "no drift"})
	}
	if err := r.Store.ReplaceDrift(t.Root, t.Env, j.id, rows); err != nil {
		r.failure(j, err, 1)
		return
	}
	resources := map[string]bool{}
	for _, row := range rows {
		resources[row.Address] = true
	}
	r.saveSummary(j.id, func(s *Summary) { s.Drift = &DriftSummary{Resources: len(resources), Attrs: len(rows)} })
	if len(resources) > 0 {
		j.emit("summary", "warn", fmt.Sprintf("Drift: %d resource(s) differ from the state Terraform recorded.", len(resources)), nil)
	} else {
		j.emit("summary", "info", "No drift: real infrastructure matches the state.", nil)
	}
	r.Bus.Publish("drift.updated", map[string]any{"root": t.Root, "env": t.Env})
	zero := 0
	r.end(j, store.StatusSucceeded, &zero, "")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
