package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/diagnostics"
	"github.com/rapando/groundwork/internal/doctor"
	"github.com/rapando/groundwork/internal/store"
	"github.com/rapando/groundwork/internal/workspace"
)

// Start runs the background services: the issue engine and the drift schedule.
func (a *API) Start(ctx context.Context) {
	a.Issues.Start(ctx)
	go func() { // warm the inventory cache: the Overview needs host counts first thing
		cfg, _ := a.config()
		for _, sc := range scopesFor(cfg) {
			c, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, _ = a.loadInventory(c, sc)
			cancel()
		}
	}()
	a.Drift.Apply()
	go func() {
		<-ctx.Done()
		a.Drift.Stop()
	}()
}

func (a *API) getIssueParam(w http.ResponseWriter, r *http.Request) (*store.Issue, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, 400, "bad_request", "invalid issue id", nil)
		return nil, false
	}
	is, err := a.Store.GetIssue(id)
	if err != nil {
		writeError(w, 404, "not_found", "no such issue", nil)
		return nil, false
	}
	return is, true
}

func weekAgo() time.Time { return time.Now().Add(-7 * 24 * time.Hour) }

func (a *API) listIssues(w http.ResponseWriter, r *http.Request) {
	open, err := a.Store.ListIssues(store.IssueOpen, time.Time{}, 200)
	if err != nil {
		writeError(w, 500, "issues_failed", err.Error(), nil)
		return
	}
	resolved, _ := a.Store.ListIssues(store.IssueResolved, weekAgo(), 20)
	views := func(is []store.Issue) []diagnostics.View {
		out := make([]diagnostics.View, 0, len(is))
		for _, i := range is {
			v := a.Issues.View(i)
			v.Excerpt = "" // the list doesn't need logs
			out = append(out, v)
		}
		return out
	}
	_, ruleErrs := a.Issues.Rules()
	var errs []string
	for _, e := range ruleErrs {
		errs = append(errs, e.Error())
	}
	writeJSON(w, 200, map[string]any{
		"open": views(open), "resolved": views(resolved),
		"resolved_this_week": a.Store.CountResolvedSince(weekAgo()),
		"rule_errors":        errs,
	})
}

func (a *API) getIssue(w http.ResponseWriter, r *http.Request) {
	is, ok := a.getIssueParam(w, r)
	if !ok {
		return
	}
	v := a.Issues.View(*is)
	if is.Status == store.IssueOpen {
		v.Checks = a.Issues.Prechecks(*is)
	}
	writeJSON(w, 200, v)
}

func (a *API) resolveIssue(w http.ResponseWriter, r *http.Request) {
	is, ok := a.getIssueParam(w, r)
	if !ok {
		return
	}
	if _, err := a.Store.ResolveIssue(is.ID, time.Now(), "marked resolved by "+currentUser()); err != nil {
		writeError(w, 500, "resolve_failed", err.Error(), nil)
		return
	}
	a.Bus.Publish("issues.updated", map[string]any{"id": is.ID})
	writeJSON(w, 200, map[string]any{"resolved": true})
}

func (a *API) actOnIssue(w http.ResponseWriter, r *http.Request) {
	is, ok := a.getIssueParam(w, r)
	if !ok {
		return
	}
	var req struct {
		Step    int    `json:"step"`
		Confirm string `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	run, err := a.Issues.Act(*is, req.Step, req.Confirm)
	if err != nil {
		code := 422
		if errors.Is(err, diagnostics.ErrConfirm) {
			code = 400
		}
		writeError(w, code, "action_failed", err.Error(), nil)
		return
	}
	writeJSON(w, 202, map[string]any{"run": brief(run)})
}

// exportIssues is a Markdown report of open and recently resolved issues, to
// paste into a ticket or chat. It holds what the UI shows: no secrets.
func (a *API) exportIssues(w http.ResponseWriter, r *http.Request) {
	open, _ := a.Store.ListIssues(store.IssueOpen, time.Time{}, 200)
	resolved, _ := a.Store.ListIssues(store.IssueResolved, weekAgo(), 50)
	var b strings.Builder
	fmt.Fprintf(&b, "# groundwork report · %s\n\nRepository: %s\n\n## Open (%d)\n\n", time.Now().Format("2006-01-02 15:04"), filepath.Base(a.Root), len(open))
	for _, is := range open {
		v := a.Issues.View(is)
		fmt.Fprintf(&b, "### %s\n\n- **Where:** %s\n- **Since:** %s\n", v.Title, v.Target, v.FirstSeen.Local().Format("2006-01-02 15:04"))
		if v.RunID != 0 {
			fmt.Fprintf(&b, "- **Run:** #%d\n", v.RunID)
		}
		fmt.Fprintf(&b, "\n%s\n\n", v.Explain)
		if v.Excerpt != "" {
			fmt.Fprintf(&b, "```\n%s\n```\n\n", v.Excerpt)
		}
	}
	fmt.Fprintf(&b, "## Resolved in the last 7 days (%d)\n\n", len(resolved))
	for _, is := range resolved {
		v := a.Issues.View(is)
		fmt.Fprintf(&b, "- %s · %s · %s\n", v.Title, v.Target, v.Resolution)
	}
	if rep := a.lastDoctor(); rep != nil {
		fmt.Fprintf(&b, "\n## Doctor (%s)\n\n", rep.RanAt.Local().Format("2006-01-02 15:04"))
		for _, c := range rep.Checks {
			fmt.Fprintf(&b, "- **%s** %s: %s\n", c.Status, c.Name, c.Detail)
		}
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="groundwork-report.md"`)
	_, _ = w.Write([]byte(b.String()))
}

// ---- doctor ----

func (a *API) lastDoctor() *doctor.Report {
	v, ok, _ := a.Store.GetKV("doctor.last")
	if !ok {
		return nil
	}
	var rep doctor.Report
	if json.Unmarshal([]byte(v), &rep) != nil {
		return nil
	}
	return &rep
}

func (a *API) getDoctor(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"report": a.lastDoctor()})
}

func (a *API) RunDoctor(ctx context.Context) doctor.Report {
	cfg, _ := a.config()
	var providers []string
	if rep, err := workspace.Detect(a.Root, a.Ignore()); err == nil {
		seen := map[string]bool{}
		for _, d := range append(rep.TerraformRoots, rep.TerraformModules...) {
			for _, p := range d.Providers {
				if !seen[p] {
					seen[p] = true
					providers = append(providers, p)
				}
			}
		}
	}
	rep := doctor.Run(ctx, doctor.Input{Root: a.Root, Cfg: cfg, Providers: providers, Store: a.Store, Exec: a.DoctorExec})
	if b, err := json.Marshal(rep); err == nil {
		_ = a.Store.SetKV("doctor.last", string(b))
	}
	a.Bus.Publish("doctor.updated", nil)
	return rep
}

func (a *API) runDoctor(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	rep := a.RunDoctor(ctx)
	writeJSON(w, 200, map[string]any{"report": rep})
}

// ---- drift schedule ----

func (a *API) driftSchedule(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.Drift.Status())
}

func (a *API) runDriftAll(w http.ResponseWriter, r *http.Request) {
	go a.Drift.RunAll(context.Background())
	writeJSON(w, 202, map[string]any{"started": true})
}

// ---- onboarding ----

type checklistItem struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Done  bool   `json:"done"`
	Href  string `json:"href"`
}

// settingsPath: ~/.config/groundwork/settings.json, or the OS config dir
// (~/Library/Application Support on macOS) if that's where the file is.
func settingsPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".config", "groundwork", "settings.json")
		if fileExists(p) {
			return p
		}
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "groundwork", "settings.json")
}

// tipsEnabled reads the global settings file; tips are on unless set to false
// there or GROUNDWORK_TIPS=off is in the environment (demos, scripted tests).
func tipsEnabled() bool {
	if v := os.Getenv("GROUNDWORK_TIPS"); v == "off" || v == "0" || v == "false" {
		return false
	}
	p := settingsPath()
	if p == "" {
		return true
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return true
	}
	var s struct {
		Tips *bool `json:"tips"`
	}
	if json.Unmarshal(b, &s) != nil || s.Tips == nil {
		return true
	}
	return *s.Tips
}

func (a *API) kvBool(k string) bool { v, ok, _ := a.Store.GetKV(k); return ok && v == "true" }

func (a *API) onboarding(w http.ResponseWriter, r *http.Request) {
	cfg, _ := a.config()
	ranChecks := false
	for _, u := range a.Checks.Status() {
		for _, t := range u.Results {
			if t.RanAt != nil {
				ranChecks = true
			}
		}
	}
	planKind, planLabel := "tf.plan", "Run your first plan"
	if cfg != nil && len(cfg.Terraform.Roots) == 0 && len(cfg.Ansible.Projects) > 0 {
		planKind, planLabel = "ans.check", "Run your first dry run"
	}
	runs, _ := a.Store.ListRuns(store.RunFilter{Kind: planKind, Limit: 1})
	_, doctorRan, _ := a.Store.GetKV("doctor.last")
	items := []checklistItem{
		{"scanned", "Scan the repository", cfg != nil, "/code"},
		{"checks", "Run the checks", ranChecks, "/code"},
		{"plan", planLabel, len(runs) > 0, "/"},
		{"visualize", "Open the infrastructure view", a.kvBool("event.visualize.opened"), "/code"},
		{"doctor", "Run doctor", doctorRan, "/troubleshoot"},
	}
	writeJSON(w, 200, map[string]any{
		"tour_completed":   a.kvBool("onboarding.tour_completed"),
		"checklist_hidden": a.kvBool("onboarding.checklist_hidden"),
		"tips":             tipsEnabled(),
		"items":            items,
	})
}

func (a *API) setOnboarding(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TourCompleted   *bool `json:"tour_completed"`
		ChecklistHidden *bool `json:"checklist_hidden"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.TourCompleted != nil {
		_ = a.Store.SetKV("onboarding.tour_completed", strconv.FormatBool(*req.TourCompleted))
	}
	if req.ChecklistHidden != nil {
		_ = a.Store.SetKV("onboarding.checklist_hidden", strconv.FormatBool(*req.ChecklistHidden))
	}
	a.onboarding(w, r)
}

var uiEvents = map[string]bool{"visualize.opened": true}

func (a *API) uiEvent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !uiEvents[req.Name] {
		writeError(w, 400, "bad_request", "unknown event", nil)
		return
	}
	_ = a.Store.SetKV("event."+req.Name, "true")
	writeJSON(w, 200, map[string]any{"ok": true})
}

// configChanged reloads groundwork.yaml after an edit on disk.
func (a *API) configChanged() {
	a.reloadConfig()
	a.Drift.Apply()
	a.Bus.Publish("workspace.updated", nil)
}

func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "you"
}
