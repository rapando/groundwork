package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rapando/groundwork/internal/ansible"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/runner"
	"github.com/rapando/groundwork/internal/store"
	"github.com/rapando/groundwork/internal/terraform"
)

func (a *API) submitRun(w http.ResponseWriter, r *http.Request) {
	var req runner.SubmitRequest
	if !decode(w, r, &req) {
		return
	}
	if cfg, _ := a.config(); cfg == nil {
		writeError(w, 409, "not_configured", "finish setup first", nil)
		return
	}
	run, err := a.Runner.Submit(req)
	if err != nil {
		switch {
		case errors.Is(err, runner.ErrBadTarget):
			writeError(w, 400, "bad_target", err.Error(), nil)
		case errors.Is(err, runner.ErrConfirmText):
			writeError(w, 422, "confirm_mismatch", err.Error(), nil)
		default:
			writeError(w, 500, "submit_failed", err.Error(), nil)
		}
		return
	}
	writeJSON(w, 202, map[string]any{"run": brief(run)})
}

// brief is the list view of a run: no per-resource plan detail.
func brief(run *store.Run) map[string]any {
	var t runner.Target
	_ = json.Unmarshal(run.Target, &t)
	var s runner.Summary
	_ = json.Unmarshal(run.Summary, &s)
	m := map[string]any{
		"id": run.ID, "kind": run.Kind, "status": run.Status, "stage": run.Stage, "commit": run.Commit,
		"created_at": run.CreatedAt, "started_at": run.StartedAt, "ended_at": run.EndedAt, "exit_code": run.ExitCode,
		"target": t, "no_changes": s.NoChanges, "error": s.Error, "apply": s.Apply,
	}
	if s.Plan != nil {
		m["counts"] = map[string]int{"create": s.Plan.Create, "update": s.Plan.Update, "replace": s.Plan.Replace, "delete": s.Plan.Delete}
	}
	return m
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("cursor"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	runs, err := a.Store.ListRuns(store.RunFilter{Kind: q.Get("kind"), Status: q.Get("status"), Before: before, Limit: limit})
	if err != nil {
		writeError(w, 500, "runs_failed", err.Error(), nil)
		return
	}
	out := make([]map[string]any, len(runs))
	for i, run := range runs {
		out[i] = brief(run)
	}
	next := ""
	if len(runs) > 0 && (limit == 0 && len(runs) == 50 || limit > 0 && len(runs) == limit) {
		next = strconv.FormatInt(runs[len(runs)-1].ID, 10)
	}
	writeJSON(w, 200, map[string]any{"runs": out, "next_cursor": next})
}

func runID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 400, "bad_request", "invalid run id", nil)
		return 0, false
	}
	return id, true
}

func runError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNoRun):
		writeError(w, 404, "not_found", "no such run", nil)
	case errors.Is(err, runner.ErrNotApprovable):
		writeError(w, 409, "not_approvable", err.Error(), nil)
	case errors.Is(err, runner.ErrNotCancelable):
		writeError(w, 409, "not_cancelable", err.Error(), nil)
	case errors.Is(err, runner.ErrPlanChanged):
		writeError(w, 409, "plan_changed", err.Error()+"; run a new plan", nil)
	case errors.Is(err, runner.ErrConfirmText):
		writeError(w, 422, "confirm_mismatch", err.Error(), nil)
	case errors.Is(err, runner.ErrNeedsAck):
		writeError(w, 422, "ack_required", err.Error(), nil)
	case errors.Is(err, runner.ErrStale):
		var se *runner.StaleError
		errors.As(err, &se)
		writeError(w, 409, "stale_plan", err.Error(), map[string]any{"reasons": se.Reasons})
	default:
		writeError(w, 500, "run_failed", err.Error(), nil)
	}
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	d, err := a.Runner.Detail(id)
	if err != nil {
		runError(w, err)
		return
	}
	writeJSON(w, 200, d)
}

func (a *API) runLog(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	if _, err := a.Store.GetRun(id); err != nil {
		runError(w, err)
		return
	}
	q := r.URL.Query()
	from, _ := strconv.Atoi(q.Get("from"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	level := q.Get("level")
	if level != "" && level != "debug" && level != "info" && level != "warn" && level != "error" {
		writeError(w, 400, "bad_request", "level must be debug, info, warn or error", nil)
		return
	}
	page, err := a.Runner.ReadLog(id, from, limit, level, q.Get("q"))
	if err != nil {
		writeError(w, 500, "log_failed", err.Error(), nil)
		return
	}
	writeJSON(w, 200, page)
}

// downloadLog serves the (already redacted) log as plain text.
func (a *API) downloadLog(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	if _, err := a.Store.GetRun(id); err != nil {
		runError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="run-`+strconv.FormatInt(id, 10)+`.log"`)
	from := 0
	for {
		page, err := a.Runner.ReadLog(id, from, 5000, "", "")
		if err != nil || len(page.Lines) == 0 {
			return
		}
		for _, l := range page.Lines {
			fmt.Fprintf(w, "%s %-9s %-5s %s\n", l.T.Local().Format("15:04:05"), l.Stage, l.Level, l.Text)
		}
		from = page.Next
	}
}

func (a *API) runResources(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	if _, err := a.Store.GetRun(id); err != nil {
		runError(w, err)
		return
	}
	res, err := a.Runner.Resources(id)
	if err != nil {
		writeError(w, 500, "log_failed", err.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]any{"resources": res})
}

// runHosts returns an ansible run's per-host task results (dry run and real run).
func (a *API) runHosts(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	if _, err := a.Store.GetRun(id); err != nil {
		runError(w, err)
		return
	}
	res, err := a.Runner.HostResults(id)
	if err != nil {
		writeError(w, 500, "hosts_failed", err.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]any{"results": res})
}

func (a *API) runPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	d, err := a.Runner.Detail(id)
	if err != nil {
		runError(w, err)
		return
	}
	if d.Summary.Plan == nil {
		writeError(w, 404, "no_plan", "this run has no plan (yet)", nil)
		return
	}
	var stale []string
	if d.Run.Status == store.StatusWaitingApproval {
		stale, _ = a.Runner.Freshness(id)
	}
	if stale == nil {
		stale = []string{}
	}
	danger := d.Summary.Danger
	if danger == nil {
		danger = []terraform.Danger{}
	}
	// validate/tflint results for the root, shown next to the approval
	checks := []map[string]any{}
	for _, u := range a.Checks.Status() {
		if u.Path == d.Target.Root {
			for _, res := range u.Results {
				checks = append(checks, map[string]any{"tool": res.Tool, "status": res.Status, "count": res.Count})
			}
		}
	}
	ttl := time.Hour
	if cfg, _ := a.config(); cfg != nil {
		ttl = cfg.Terraform.PlanTTLDuration()
	}
	writeJSON(w, 200, map[string]any{
		"plan": d.Summary.Plan, "no_changes": d.Summary.NoChanges, "status": d.Run.Status,
		"target": d.Target, "approvable": d.Run.Status == store.StatusWaitingApproval && len(stale) == 0,
		"confirm_text": confirmTextFor(d), "danger": danger, "stale": stale,
		"state": d.Summary.State, "planned_at": d.Summary.PlannedAt, "plan_ttl_seconds": int(ttl.Seconds()),
		"commit": d.Run.Commit, "plan_file": ".groundwork/runs/" + strconv.FormatInt(id, 10) + "/tfplan",
		"checks": checks, "approval": d.Approval, "apply": d.Summary.Apply, "error": d.Summary.Error,
	})
}

// planResource returns one resource's masked attribute diff (lazy-loaded by the UI).
func (a *API) planResource(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	if _, err := a.Store.GetRun(id); err != nil {
		runError(w, err)
		return
	}
	diff, loc, err := a.Runner.ResourceDiff(id, r.URL.Query().Get("address"))
	if err != nil {
		writeError(w, 404, "not_found", err.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]any{"diff": diff, "location": loc})
}

func (a *API) replanRun(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	run, err := a.Runner.Replan(id)
	if err != nil {
		if errors.Is(err, runner.ErrBadTarget) {
			writeError(w, 400, "bad_target", err.Error(), nil)
			return
		}
		runError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"run": brief(run)})
}

// confirmTextFor is what the user must type (empty when no typing is needed).
func confirmTextFor(d *runner.Detail) string {
	if d.Target.ApprovalRequired {
		return d.Target.Env
	}
	return ""
}

func (a *API) cancelRun(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	if err := a.Runner.Cancel(id); err != nil {
		runError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"cancelling": true})
}

func (a *API) approveRun(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	var req runner.ApproveRequest
	if !decode(w, r, &req) {
		return
	}
	if err := a.Runner.Approve(id, req); err != nil {
		runError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"approved": true})
}

// ---- environments (Overview) ----

type pending struct {
	RunID   int64 `json:"run_id"`
	Create  int   `json:"create"`
	Update  int   `json:"update"`
	Replace int   `json:"replace"`
	Delete  int   `json:"delete"`
}

type envTarget struct {
	Root             string         `json:"root"`
	Env              string         `json:"env"`
	ApprovalRequired bool           `json:"approval_required"`
	LastRun          map[string]any `json:"last_run,omitempty"`
	LastPlanClean    *time.Time     `json:"in_sync_since,omitempty"` // latest plan found no changes
	Pending          *pending       `json:"pending,omitempty"`       // an approvable plan exists
	Running          bool           `json:"running"`
	Drift            int            `json:"drift"` // resources that differ from state
}

type ansibleTarget struct {
	Project string         `json:"project"`
	Hosts   int            `json:"hosts"` // -1 = unknown (inventory couldn't be read)
	Down    int            `json:"down"`
	LastRun map[string]any `json:"last_run,omitempty"`
}

type envView struct {
	Name    string          `json:"name"`
	Targets []envTarget     `json:"targets"`
	Ansible []ansibleTarget `json:"ansible"`
	Status  string          `json:"status"` // failed | pending | running | ok | unknown
}

func (a *API) listEnvs(w http.ResponseWriter, r *http.Request) {
	cfg, _ := a.config()
	if cfg == nil {
		writeJSON(w, 200, map[string]any{"envs": []envView{}})
		return
	}
	runs, err := a.Store.ListRuns(store.RunFilter{Limit: 500})
	if err != nil {
		writeError(w, 500, "runs_failed", err.Error(), nil)
		return
	}
	type key struct{ root, env string }
	latest := map[key]*store.Run{}     // newest run of any kind
	latestPlan := map[key]*store.Run{} // newest plan run
	for _, run := range runs {         // newest first
		var t runner.Target
		_ = json.Unmarshal(run.Target, &t)
		k := key{t.Root, t.Env}
		if latest[k] == nil {
			latest[k] = run
		}
		if run.Kind == runner.KindPlan && latestPlan[k] == nil {
			latestPlan[k] = run
		}
	}
	byEnv := map[string]*envView{}
	add := func(root, env string) {
		t, err := runner.ResolveTarget(cfg, root, env)
		if err != nil {
			return
		}
		et := envTarget{Root: root, Env: env, ApprovalRequired: t.ApprovalRequired}
		if rows, err := a.Store.ListDrift(root, env); err == nil {
			seen := map[string]bool{}
			for _, row := range rows {
				seen[row.Address] = true
			}
			et.Drift = len(seen)
		}
		k := key{root, env}
		if run := latest[k]; run != nil {
			et.LastRun = brief(run)
			et.Running = run.Status == store.StatusRunning || run.Status == store.StatusQueued
		}
		if run := latestPlan[k]; run != nil {
			var s runner.Summary
			_ = json.Unmarshal(run.Summary, &s)
			switch {
			case run.Status == store.StatusWaitingApproval && s.Plan != nil:
				et.Pending = &pending{RunID: run.ID, Create: s.Plan.Create, Update: s.Plan.Update, Replace: s.Plan.Replace, Delete: s.Plan.Delete}
			case run.Status == store.StatusSucceeded && s.NoChanges && run.EndedAt != nil:
				et.LastPlanClean = run.EndedAt
			}
		}
		v := byEnv[env]
		if v == nil {
			v = &envView{Name: env}
			byEnv[env] = v
		}
		v.Targets = append(v.Targets, et)
	}
	for _, root := range cfg.Terraform.Roots {
		if root.Env != "" {
			add(trimSlash(root.Path), root.Env)
		}
		for env := range root.Envs {
			add(trimSlash(root.Path), env)
		}
	}
	// ansible scopes join the same environments by name; inventories load in parallel
	scopes := scopesFor(cfg)
	invs := make([]*ansible.Inventory, len(scopes))
	var wg sync.WaitGroup
	for i, sc := range scopes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			if inv, err := a.loadInventory(ctx, sc); err == nil {
				invs[i] = inv
			}
		}()
	}
	wg.Wait()
	for i, sc := range scopes {
		at := ansibleTarget{Project: sc.Project, Hosts: -1}
		if inv := invs[i]; inv != nil {
			at.Hosts = len(inv.Hosts)
			if st, err := a.Store.HostStatuses(sc.Project + "#" + sc.Env); err == nil {
				for _, h := range inv.Hosts {
					if s, ok := st[h]; ok && !s.Reachable {
						at.Down++
					}
				}
			}
		}
		for _, run := range runs { // newest first
			if !strings.HasPrefix(run.Kind, "ans.") || run.Kind == runner.KindAnsPing || run.Kind == runner.KindAnsFacts {
				continue
			}
			var t runner.Target
			_ = json.Unmarshal(run.Target, &t)
			if t.Project == sc.Project && t.Env == sc.Env {
				at.LastRun = brief(run)
				break
			}
		}
		v := byEnv[sc.Env]
		if v == nil {
			v = &envView{Name: sc.Env}
			byEnv[sc.Env] = v
		}
		v.Ansible = append(v.Ansible, at)
	}
	out := make([]envView, 0, len(byEnv))
	for _, v := range byEnv {
		if v.Targets == nil {
			v.Targets = []envTarget{}
		}
		if v.Ansible == nil {
			v.Ansible = []ansibleTarget{}
		}
		sort.Slice(v.Targets, func(i, j int) bool { return v.Targets[i].Root < v.Targets[j].Root })
		v.Status = "unknown"
		for _, t := range v.Targets {
			lr := t.LastRun
			switch {
			case t.Running:
				v.Status = "running"
			case lr != nil && lr["status"] == store.StatusFailed && v.Status != "running":
				v.Status = "failed"
			case t.Pending != nil && v.Status != "failed" && v.Status != "running":
				v.Status = "pending"
			case t.Drift > 0 && v.Status != "failed" && v.Status != "running" && v.Status != "pending":
				v.Status = "drift"
			case (lr != nil || t.LastPlanClean != nil) && v.Status == "unknown":
				v.Status = "ok"
			}
		}
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		return envRank(out[i].Name) < envRank(out[j].Name) || envRank(out[i].Name) == envRank(out[j].Name) && out[i].Name < out[j].Name
	})
	writeJSON(w, 200, map[string]any{"envs": out})
}

func trimSlash(p string) string { return strings.TrimSuffix(p, "/") }

func envRank(n string) int {
	switch n {
	case "dev":
		return 0
	case "test", "qa":
		return 1
	case "staging":
		return 2
	case "prod":
		return 3
	}
	return 4
}
