package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zclconf/go-cty/cty"

	"github.com/rapando/groundwork/internal/files"
	"github.com/rapando/groundwork/internal/graph"
	"github.com/rapando/groundwork/internal/runner"
	"github.com/rapando/groundwork/internal/store"
	"github.com/rapando/groundwork/internal/terraform"
)

// varValues: defaults are read by the graph builder; tfvars of the chosen
// environment (terraform.tfvars, *.auto.tfvars, then configured var files).
func (a *API) varValues(t runner.Target) map[string]cty.Value {
	dir := filepath.Join(a.Root, filepath.FromSlash(t.Root))
	files := []string{filepath.Join(dir, "terraform.tfvars")}
	auto, _ := filepath.Glob(filepath.Join(dir, "*.auto.tfvars"))
	sort.Strings(auto)
	files = append(files, auto...)
	for _, vf := range t.VarFiles {
		files = append(files, filepath.Join(a.Root, filepath.FromSlash(vf)))
	}
	return graph.VarsFromFiles(files...)
}

var dotCache sync.Map // root dir → {fp string, edges []graph.Edge}

type dotEntry struct {
	fp    string
	edges []graph.Edge
}

// terraformGraph runs `terraform graph` when the root has been initialised;
// otherwise static analysis stands alone.
func (a *API) terraformGraph(ctx context.Context, t runner.Target) []graph.Edge {
	dir := filepath.Join(a.Root, filepath.FromSlash(t.Root))
	if _, err := os.Stat(filepath.Join(dir, ".terraform")); err != nil {
		return nil
	}
	fp := projectFingerprint(dir)
	if e, ok := dotCache.Load(dir); ok && e.(dotEntry).fp == fp {
		return e.(dotEntry).edges
	}
	bin, err := exec.LookPath(t.Binary)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, terraform.GraphArgs(t.Binary, dir)[1:]...)
	cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1", "NO_COLOR=1", "CHECKPOINT_DISABLE=1")
	if t.Workspace != "" {
		cmd.Env = append(cmd.Env, "TF_WORKSPACE="+t.Workspace)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	edges := graph.ParseDOT(string(out))
	dotCache.Store(dir, dotEntry{fp, edges})
	return edges
}

func (a *API) graphTarget(w http.ResponseWriter, root, env string) (runner.Target, bool) {
	cfg, _ := a.config()
	t, err := runner.ResolveTarget(cfg, root, env)
	if err != nil {
		writeError(w, 400, "bad_target", err.Error(), nil)
		return t, false
	}
	return t, true
}

// overlays: the latest approvable plan's actions, and recorded drift.
func (a *API) overlays(t runner.Target) (actions map[string]string, drifted map[string]bool, planRun int64) {
	actions, drifted = map[string]string{}, map[string]bool{}
	runs, _ := a.Store.ListRuns(store.RunFilter{Kind: runner.KindPlan, Limit: 100})
	for _, run := range runs {
		var rt runner.Target
		_ = json.Unmarshal(run.Target, &rt)
		if rt.Root != t.Root || rt.Env != t.Env {
			continue
		}
		if run.Status == store.StatusWaitingApproval {
			var s runner.Summary
			_ = json.Unmarshal(run.Summary, &s)
			if s.Plan != nil {
				planRun = run.ID
				for _, c := range s.Plan.Changes {
					actions[graph.BaseAddress(c.Address)] = c.Action
				}
			}
		}
		break // only the newest plan counts
	}
	rows, _ := a.Store.ListDrift(t.Root, t.Env)
	for _, r := range rows {
		drifted[graph.BaseAddress(r.Address)] = true
	}
	return
}

func (a *API) graphDeps(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	t, ok := a.graphTarget(w, q.Get("root"), q.Get("env"))
	if !ok {
		return
	}
	g := graph.Build(graph.Options{RepoRoot: a.Root, RootDir: filepath.Join(a.Root, filepath.FromSlash(t.Root)), VarVals: a.varValues(t)})
	if edges := a.terraformGraph(r.Context(), t); edges != nil {
		g.MergeDOT(edges)
	}
	actions, drifted, planRun := a.overlays(t)
	g.Overlay(actions, drifted)
	a.markErrors(g)
	writeJSON(w, 200, map[string]any{"graph": g, "plan_run": planRun, "target": t})
}

func (a *API) markErrors(g *graph.Graph) {
	diags, _ := a.Checks.Diagnostics("", "error", "")
	var ds []struct {
		File string
		Line int
	}
	for _, d := range diags {
		ds = append(ds, struct {
			File string
			Line int
		}{d.File, d.Line})
	}
	g.MarkErrors(ds)
}

type archReq struct {
	Root    string            `json:"root"`
	Env     string            `json:"env"`
	Buffers map[string]string `json:"buffers"` // repo-relative path → unsaved content
}

// graphArchitecture is computed from the editor's unsaved buffers, so the
// diagram follows typing without saving. Pure static analysis: no terraform.
func (a *API) graphArchitecture(w http.ResponseWriter, r *http.Request) {
	var req archReq
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON body", nil)
		return
	}
	t, ok := a.graphTarget(w, req.Root, req.Env)
	if !ok {
		return
	}
	bufs := map[string][]byte{}
	for p, content := range req.Buffers {
		// SafePath validates; the key must match how the builder names files
		// (unresolved), or a symlinked repo path (macOS /var → /private/var)
		// would silently ignore every unsaved buffer.
		if _, err := files.SafePath(a.Root, p); err != nil {
			writeError(w, 400, "bad_path", err.Error(), nil)
			return
		}
		bufs[filepath.Join(a.Root, filepath.FromSlash(filepath.Clean(p)))] = []byte(content)
	}
	g := graph.Build(graph.Options{RepoRoot: a.Root, RootDir: filepath.Join(a.Root, filepath.FromSlash(t.Root)), Buffers: bufs, VarVals: a.varValues(t)})
	actions, drifted, _ := a.overlays(t)
	g.Overlay(actions, drifted)
	a.markErrors(g)
	writeJSON(w, 200, map[string]any{"architecture": graph.BuildArchitecture(g, graph.LoadRules()), "modules": g.Modules})
}

// graphRoots lists the roots (and envs) a file belongs to, for "as used by".
func (a *API) graphRoots(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	if file != "" {
		if _, err := files.SafePath(a.Root, file); err != nil {
			fileError(w, err)
			return
		}
	}
	cfg, _ := a.config()
	type choice struct {
		Root string `json:"root"`
		Env  string `json:"env"`
	}
	out := []choice{}
	if cfg == nil {
		writeJSON(w, 200, map[string]any{"roots": out})
		return
	}
	units := a.Checks.Units()
	under := func(dir, f string) bool { return dir == "." || f == dir || strings.HasPrefix(f, dir+"/") }
	for _, u := range units {
		if u.Kind != "terraform-root" {
			continue
		}
		match := file == "" || under(u.Path, file)
		for _, d := range u.Deps {
			match = match || under(d, file)
		}
		if !match {
			continue
		}
		for _, root := range cfg.Terraform.Roots {
			if filepath.Clean(root.Path) != u.Path {
				continue
			}
			if root.Env != "" {
				out = append(out, choice{u.Path, root.Env})
			}
			envs := make([]string, 0, len(root.Envs))
			for e := range root.Envs {
				envs = append(envs, e)
			}
			sort.Slice(envs, func(i, j int) bool { return envRank(envs[i]) < envRank(envs[j]) })
			for _, e := range envs {
				out = append(out, choice{u.Path, e})
			}
		}
	}
	writeJSON(w, 200, map[string]any{"roots": out})
}

// ---- drift ----

type driftResource struct {
	Address    string           `json:"address"`
	Action     string           `json:"action"`
	DetectedAt time.Time        `json:"detected_at"`
	RunID      int64            `json:"run_id"`
	Attrs      []store.DriftRow `json:"attrs"`
}

func (a *API) listDrift(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, err := a.Store.ListDrift(q.Get("root"), q.Get("env"))
	if err != nil {
		writeError(w, 500, "drift_failed", err.Error(), nil)
		return
	}
	by := map[string]*driftResource{}
	var order []string
	for _, row := range rows {
		k := row.Root + "|" + row.Env + "|" + row.Address
		if by[k] == nil {
			by[k] = &driftResource{Address: row.Address, Action: row.Action, DetectedAt: row.DetectedAt, RunID: row.RunID, Attrs: []store.DriftRow{}}
			order = append(order, k)
		}
		by[k].Attrs = append(by[k].Attrs, row)
	}
	out := make([]*driftResource, 0, len(order))
	for _, k := range order {
		out = append(out, by[k])
	}
	writeJSON(w, 200, map[string]any{"resources": out})
}

type driftActionReq struct {
	Action string `json:"action"` // copy | ignore | revert
	Apply  bool   `json:"apply"`  // false = preview
	SHA    string `json:"sha"`    // the file sha the preview was made from
}

// driftAction previews or applies one of the three drift fixes. copy and
// ignore edit code (shown as a diff first, written with If-Match);
// revert starts an ordinary plan.
func (a *API) driftAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, 400, "bad_request", "invalid id", nil)
		return
	}
	var req driftActionReq
	if !decode(w, r, &req) {
		return
	}
	row, err := a.Store.GetDrift(id)
	if err != nil {
		writeError(w, 404, "not_found", err.Error(), nil)
		return
	}
	if req.Action == "revert" {
		run, err := a.Runner.Submit(runner.SubmitRequest{Kind: runner.KindPlan, Root: row.Root, Env: row.Env})
		if err != nil {
			runError(w, err)
			return
		}
		writeJSON(w, 202, map[string]any{"run": brief(run)})
		return
	}
	if req.Action != "copy" && req.Action != "ignore" {
		writeError(w, 400, "bad_request", "action must be copy, ignore or revert", nil)
		return
	}
	if row.Action == "delete" {
		writeError(w, 422, "not_applicable", "the resource was deleted outside Terraform; revert (plan) recreates it", nil)
		return
	}
	loc := terraform.Locate(a.Root, filepath.Join(a.Root, filepath.FromSlash(row.Root)), row.Address)
	if loc == nil {
		writeError(w, 422, "not_found", "can't find where "+row.Address+" is declared", nil)
		return
	}
	cur, err := files.Read(a.Root, loc.File)
	if err != nil {
		fileError(w, err)
		return
	}
	base := graph.BaseAddress(row.Address)
	parts := strings.Split(base, ".")
	typ, name := parts[len(parts)-2], parts[len(parts)-1]
	var next []byte
	switch req.Action {
	case "copy":
		if strings.ContainsAny(row.AttrPath, ".[") {
			writeError(w, 422, "not_simple", "only top-level string, number and bool arguments can be copied; edit "+row.AttrPath+" in the editor", nil)
			return
		}
		v, ok := terraform.ValueFromRendered(row.Actual)
		if !ok {
			writeError(w, 422, "not_simple", "the actual value isn't a simple string, number or bool", nil)
			return
		}
		next, err = terraform.SetAttribute([]byte(cur.Text), loc.File, typ, name, row.AttrPath, v)
	case "ignore":
		next, err = terraform.AddIgnoreChanges([]byte(cur.Text), loc.File, typ, name, terraform.RootAttr(row.AttrPath))
	}
	if err != nil {
		writeError(w, 422, "edit_failed", err.Error(), nil)
		return
	}
	diff := files.UnifiedDiff(loc.File, cur.Text, string(next))
	if !req.Apply {
		writeJSON(w, 200, map[string]any{"file": loc.File, "line": loc.Line, "diff": diff, "sha": cur.SHA})
		return
	}
	if req.SHA != cur.SHA {
		writeError(w, 409, "conflict", "the file changed since the preview; preview again", nil)
		return
	}
	sha, err := files.Write(a.Root, loc.File, next, cur.SHA, false)
	if err != nil {
		var ce *files.ConflictError
		if errors.As(err, &ce) {
			writeError(w, 409, "conflict", "the file changed since the preview; preview again", nil)
			return
		}
		fileError(w, err)
		return
	}
	a.Checks.RunNow([]string{loc.File})
	writeJSON(w, 200, map[string]any{"file": loc.File, "sha": sha, "written": true})
}
