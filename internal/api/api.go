// Package api implements the /api HTTP handlers.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/rapando/groundwork/internal/checks"
	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/diagnostics"
	"github.com/rapando/groundwork/internal/doctor"
	"github.com/rapando/groundwork/internal/drift"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/runner"
	"github.com/rapando/groundwork/internal/scaffold"
	"github.com/rapando/groundwork/internal/store"
	"github.com/rapando/groundwork/internal/vars"
	"github.com/rapando/groundwork/internal/workspace"
)

type API struct {
	Root    string
	Version string
	Bus     *events.Bus
	Store   *store.Store
	Checks  *checks.Service
	Runner  *runner.Runner

	mu     sync.RWMutex
	cfg    *config.Config
	cfgErr error

	toolsOnce sync.Once
	tools     []doctor.Tool

	secrets *vars.Secrets

	Issues     *diagnostics.Engine
	Drift      *drift.Scheduler
	DoctorExec doctor.Exec // nil: the real one
}

func New(root, version string, bus *events.Bus, st *store.Store) *API {
	a := &API{Root: root, Version: version, Bus: bus, Store: st}
	a.reloadConfig()
	a.Runner = runner.New(root, st, bus, func() *config.Config { c, _ := a.config(); return c }, nil)
	a.Checks = checks.New(root, func() *config.Config { c, _ := a.config(); return c }, checks.OSExec{}, st, bus, nil)
	a.Issues = diagnostics.New(root, st, a.Runner, a.Checks, bus, nil)
	a.Drift = drift.New(a.Runner, st, func() *config.Config { c, _ := a.config(); return c }, nil)
	return a
}

// Ignore returns the configured ignore patterns (nil before setup).
func (a *API) Ignore() []string {
	if c, _ := a.config(); c != nil {
		return c.Ignore
	}
	return nil
}

func (a *API) reloadConfig() {
	c, err := config.Load(a.Root)
	if errors.Is(err, os.ErrNotExist) {
		c, err = nil, nil
	}
	a.mu.Lock()
	a.cfg, a.cfgErr = c, err
	a.mu.Unlock()
}

func (a *API) config() (*config.Config, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg, a.cfgErr
}

// Routes mounts handlers under the /api router.
func (a *API) Routes(r chi.Router) {
	r.Get("/workspace", a.workspace)
	r.Get("/files", a.listFiles)
	r.Get("/files/content", a.readFile)
	r.Put("/files/content", a.writeFile)
	r.Get("/git/status", a.gitStatus)
	r.Get("/git/diff", a.gitDiff)
	r.Get("/checks", a.listChecks)
	r.Post("/checks/run", a.runChecks)
	r.Post("/checks/fix", a.applyFix)
	r.Get("/envs", a.listEnvs)
	r.Get("/inventory", a.getInventory)
	r.Get("/graph/deps", a.graphDeps)
	r.Get("/graph/roots", a.graphRoots)
	r.Post("/graph/architecture", a.graphArchitecture)
	r.Get("/drift", a.listDrift)
	r.Post("/drift/{id}/action", a.driftAction)
	r.Get("/inventory/host", a.getInventoryHost)
	r.Get("/inventory/var-targets", a.getVarTargets)
	r.Post("/inventory/edit", a.editInventory)
	r.Get("/vars/terraform", a.terraformVars)
	r.Put("/vars/terraform", a.setTerraformVar)
	r.Get("/vars/ansible", a.ansibleVars)
	r.Get("/secrets", a.listSecrets)
	r.Post("/secrets/reveal", a.revealSecret)
	r.Post("/secrets/scan", a.scanSecrets)
	r.Post("/secrets/move", a.moveSecret)
	r.Get("/issues", a.listIssues)
	r.Get("/issues/export", a.exportIssues)
	r.Get("/issues/{id}", a.getIssue)
	r.Post("/issues/{id}/resolve", a.resolveIssue)
	r.Post("/issues/{id}/act", a.actOnIssue)
	r.Get("/doctor", a.getDoctor)
	r.Post("/doctor/run", a.runDoctor)
	r.Get("/drift/schedule", a.driftSchedule)
	r.Post("/drift/run", a.runDriftAll)
	r.Get("/onboarding", a.onboarding)
	r.Post("/onboarding", a.setOnboarding)
	r.Post("/onboarding/event", a.uiEvent)
	r.Get("/runs", a.listRuns)
	r.Post("/runs", a.submitRun)
	r.Get("/runs/{id}", a.getRun)
	r.Get("/runs/{id}/log", a.runLog)
	r.Get("/runs/{id}/log/download", a.downloadLog)
	r.Get("/runs/{id}/resources", a.runResources)
	r.Get("/runs/{id}/hosts", a.runHosts)
	r.Get("/runs/{id}/plan", a.runPlan)
	r.Get("/runs/{id}/plan/resource", a.planResource)
	r.Post("/runs/{id}/replan", a.replanRun)
	r.Post("/runs/{id}/cancel", a.cancelRun)
	r.Post("/runs/{id}/approve", a.approveRun)
	r.Get("/setup/detect", a.setupDetect)
	r.Post("/setup/config", a.setupConfig)
	r.Post("/setup/apply", a.setupApply)
	r.Post("/setup/scaffold/preview", a.scaffoldPreview)
	r.Post("/setup/scaffold", a.scaffoldWrite)
}

// ---- workspace ----

func (a *API) workspace(w http.ResponseWriter, r *http.Request) {
	cfg, cfgErr := a.config()
	a.toolsOnce.Do(func() { a.tools = doctor.DetectTools(context.Background()) })
	out := map[string]any{
		"root":       a.Root,
		"name":       filepath.Base(a.Root),
		"version":    a.Version,
		"configured": cfg != nil,
		"tools":      a.tools,
		"git":        isGitRepo(a.Root),
	}
	if cfgErr != nil {
		out["config_error"] = cfgErr.Error()
	}
	switch {
	case cfg != nil:
		out["setup"] = "none"
		out["mode"] = cfg.Mode
		out["config"] = configJSON(cfg)
	default:
		rep, err := workspace.Detect(a.Root, nil)
		if err != nil {
			writeError(w, 500, "detect_failed", err.Error(), nil)
			return
		}
		if rep.HasIaC() {
			out["setup"] = "detect"
		} else {
			out["setup"] = "scaffold"
		}
	}
	writeJSON(w, 200, out)
}

// configJSON converts the config to generic JSON via YAML so field names
// match groundwork.yaml rather than Go identifiers.
func configJSON(c *config.Config) any {
	b, _ := yaml.Marshal(c)
	var v any
	_ = yaml.Unmarshal(b, &v)
	return v
}

func isGitRepo(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil
}

// ---- setup: detect ----

func (a *API) setupDetect(w http.ResponseWriter, r *http.Request) {
	rep, err := workspace.Detect(a.Root, nil)
	if err != nil {
		writeError(w, 500, "detect_failed", err.Error(), nil)
		return
	}
	cfg := config.FromReport(rep)
	y, err := cfg.Marshal()
	if err != nil {
		writeError(w, 500, "config_failed", err.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]any{
		"report":      rep,
		"config_yaml": string(y),
		"exists":      fileExists(filepath.Join(a.Root, config.FileName)),
	})
}

// setupConfig renders groundwork.yaml for the Detect screen's selections
// (nothing is written).
func (a *API) setupConfig(w http.ResponseWriter, r *http.Request) {
	var sel config.Selection
	if !decode(w, r, &sel) {
		return
	}
	rep, err := workspace.Detect(a.Root, nil)
	if err != nil {
		writeError(w, 500, "detect_failed", err.Error(), nil)
		return
	}
	y, err := config.FromReport(rep).Apply(sel).Marshal()
	if err != nil {
		writeError(w, 500, "config_failed", err.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]any{"config_yaml": string(y)})
}

type applyReq struct {
	YAML        string             `json:"yaml"` // edited config; defaults to the detected one
	LintConfigs []scaffold.LintReq `json:"lint_configs"`
	Gitignore   bool               `json:"gitignore"` // also add .groundwork/ to .gitignore
	Overwrite   bool               `json:"overwrite"`
}

func (a *API) setupApply(w http.ResponseWriter, r *http.Request) {
	var req applyReq
	if !decode(w, r, &req) {
		return
	}
	if req.YAML == "" {
		rep, err := workspace.Detect(a.Root, nil)
		if err != nil {
			writeError(w, 500, "detect_failed", err.Error(), nil)
			return
		}
		b, err := config.FromReport(rep).Marshal()
		if err != nil {
			writeError(w, 500, "config_failed", err.Error(), nil)
			return
		}
		req.YAML = string(b)
	}
	cfg, err := config.Parse([]byte(req.YAML), config.FileName)
	if err != nil {
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			writeError(w, 400, "invalid_config", "groundwork.yaml has problems", ve.Problems)
			return
		}
		writeError(w, 400, "invalid_config", err.Error(), nil)
		return
	}
	files := []scaffold.File{{Path: config.FileName, Content: req.YAML}}
	lint, err := scaffold.LintConfigs(&scaffold.Form{Preset: "aws-envs"}, req.LintConfigs)
	if err != nil {
		writeError(w, 400, "bad_request", err.Error(), nil)
		return
	}
	files = append(files, lint...)
	if req.Gitignore {
		files = append(files, scaffold.GitignoreEntry())
	}
	ow := map[string]bool{}
	if req.Overwrite {
		ow[config.FileName] = true
	}
	if !req.Overwrite && fileExists(filepath.Join(a.Root, config.FileName)) {
		writeError(w, 409, "exists", "groundwork.yaml already exists", nil)
		return
	}
	res, err := scaffold.Apply(a.Root, files, ow)
	if err != nil {
		writeError(w, 500, "write_failed", err.Error(), nil)
		return
	}
	a.reloadConfig()
	a.Bus.Publish("workspace.updated", nil)
	writeJSON(w, 200, map[string]any{"results": res, "mode": cfg.Mode})
}

// ---- setup: scaffold ----

type scaffoldReq struct {
	Form      scaffold.Form `json:"form"`
	Overwrite []string      `json:"overwrite"`
	GitCommit bool          `json:"git_commit"`
}

func (a *API) scaffoldPreview(w http.ResponseWriter, r *http.Request) {
	var req scaffoldReq
	if !decode(w, r, &req) {
		return
	}
	files, ok := a.render(w, &req.Form)
	if !ok {
		return
	}
	planned, err := scaffold.Plan(a.Root, files)
	if err != nil {
		writeError(w, 500, "plan_failed", err.Error(), nil)
		return
	}
	existing := 0
	for _, p := range planned {
		if p.Exists {
			existing++
		}
	}
	writeJSON(w, 200, map[string]any{
		"files": planned, "count": len(planned), "existing": existing, "new": len(planned) - existing,
	})
}

func (a *API) scaffoldWrite(w http.ResponseWriter, r *http.Request) {
	var req scaffoldReq
	if !decode(w, r, &req) {
		return
	}
	files, ok := a.render(w, &req.Form)
	if !ok {
		return
	}
	if r.URL.Query().Get("format") == "zip" {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+req.Form.Project+`-iac.zip"`)
		if err := scaffold.WriteZip(w, files); err != nil {
			// headers already sent; nothing useful to add
			return
		}
		return
	}
	if req.GitCommit && !isGitRepo(a.Root) {
		writeError(w, 400, "not_git", "initial commit requested but this directory is not a git repository", nil)
		return
	}
	ow := map[string]bool{}
	for _, p := range req.Overwrite {
		ow[p] = true
	}
	res, err := scaffold.Apply(a.Root, files, ow)
	if err != nil {
		writeError(w, 500, "write_failed", err.Error(), nil)
		return
	}
	out := map[string]any{"results": res}
	if req.GitCommit {
		var paths []string
		for _, x := range res {
			if x.Status != "skipped" {
				paths = append(paths, x.Path)
			}
		}
		if msg, err := gitCommit(r.Context(), a.Root, paths); err != nil {
			out["git_error"] = err.Error() // files are written; surface, don't fail
		} else {
			out["git_commit"] = msg
		}
	}
	a.reloadConfig()
	a.Bus.Publish("workspace.updated", nil)
	writeJSON(w, 200, out)
}

func (a *API) render(w http.ResponseWriter, f *scaffold.Form) ([]scaffold.File, bool) {
	f.Normalize()
	if errs := f.Validate(); len(errs) > 0 {
		writeError(w, 422, "invalid_form", "the form has problems", errs)
		return nil, false
	}
	files, err := scaffold.Render(f)
	if err != nil {
		writeError(w, 500, "render_failed", err.Error(), nil)
		return nil, false
	}
	return files, true
}

// gitCommit commits only the given paths (leaves other staged work alone).
func gitCommit(ctx context.Context, root string, paths []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
		b, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", args[0], err, b)
		}
		return string(b), nil
	}
	if len(paths) == 0 {
		return "nothing to commit", nil
	}
	if _, err := run(append([]string{"add", "--"}, paths...)...); err != nil {
		return "", err
	}
	if _, err := run(append([]string{"commit", "-m", "chore: scaffold with groundwork", "--"}, paths...)...); err != nil {
		return "", err
	}
	return "chore: scaffold with groundwork", nil
}

// ---- helpers ----

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON body: "+err.Error(), nil)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string, details any) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": msg, "details": details}})
}

func absPath(p string) (string, error) { return filepath.Abs(p) }

func itoa(n int) string { return strconv.Itoa(n) }

// Summary is the project card on the service's project list.
type Summary struct {
	Configured  bool   `json:"configured"`
	ConfigError string `json:"config_error,omitempty"`
	ActiveRuns  int    `json:"active_runs"`
	OpenIssues  int    `json:"open_issues"`
}

func (a *API) Summary() Summary {
	cfg, err := a.config()
	s := Summary{Configured: cfg != nil, ActiveRuns: a.Runner.Active()}
	if err != nil {
		s.ConfigError = err.Error()
	}
	if open, err := a.Store.ListIssues(store.IssueOpen, time.Time{}, 1000); err == nil {
		s.OpenIssues = len(open)
	}
	return s
}
