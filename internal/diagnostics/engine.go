package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/rapando/groundwork/internal/checks"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/runner"
	"github.com/rapando/groundwork/internal/store"
)

// Context is what an issue is about; templates see it as .Target, .Root, ….
type Context struct {
	Target  string            `json:"target_label"`
	Root    string            `json:"root,omitempty"`
	Env     string            `json:"env,omitempty"`
	Project string            `json:"project,omitempty"`
	Host    string            `json:"host,omitempty"`
	RunID   int64             `json:"run_id,omitempty"`
	RunKind string            `json:"run_kind,omitempty"`
	Vars    map[string]string `json:"vars,omitempty"`
	Excerpt string            `json:"excerpt,omitempty"`
}

func (c Context) data(vars map[string]string) map[string]any {
	d := map[string]any{
		"Target": c.Target, "Root": c.Root, "Env": c.Env, "Project": c.Project, "Host": c.Host,
		"Run": map[string]any{"ID": c.RunID, "Kind": c.RunKind},
	}
	for k, v := range vars {
		d[k] = v
	}
	return d
}

// funcs available to rule templates: path joins non-empty parts ("/" separated).
var funcs = template.FuncMap{"path": func(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return path.Join(keep...)
}}

func render(tpl string, data map[string]any) string {
	if tpl == "" {
		return ""
	}
	t, err := template.New("").Funcs(funcs).Parse(tpl)
	if err != nil {
		return tpl
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return tpl
	}
	return strings.TrimSpace(strings.ReplaceAll(b.String(), "<no value>", ""))
}

// View is an issue rendered for the UI.
type View struct {
	ID         int64             `json:"id"`
	RuleID     string            `json:"rule_id"`
	Category   string            `json:"category"`
	Severity   string            `json:"severity"`
	Title      string            `json:"title"`
	Explain    string            `json:"explain"`
	Target     string            `json:"target"`
	Status     string            `json:"status"`
	FirstSeen  time.Time         `json:"first_seen"`
	LastSeen   time.Time         `json:"last_seen"`
	ResolvedAt *time.Time        `json:"resolved_at,omitempty"`
	Resolution string            `json:"resolution,omitempty"`
	RunID      int64             `json:"run_id,omitempty"`
	File       string            `json:"file,omitempty"`
	Line       string            `json:"line,omitempty"`
	Facts      map[string]string `json:"facts,omitempty"`
	Excerpt    string            `json:"excerpt,omitempty"`
	Checks     []CheckResult     `json:"checks,omitempty"`
	Steps      []Step            `json:"steps"`
}

type CheckResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Render fills a rule's templates for one hit.
func Render(r *Rule, h Hit, c Context) View {
	vars := h.Vars
	if c.Host == "" {
		c.Host = vars["host"]
	}
	d := c.data(vars)
	v := View{RuleID: r.ID, Category: r.Category, Severity: r.Severity, Title: render(r.Title, d), Explain: render(r.Explain, d),
		Target: c.Target, RunID: c.RunID, Excerpt: h.Excerpt, File: vars["file"], Line: vars["line"], Facts: map[string]string{}}
	for k, val := range vars {
		if val != "" && k != "host" && k != "message" && k != "tool" && k != "code" {
			v.Facts[k] = val
		}
	}
	for _, s := range r.Steps {
		st := Step{Title: render(s.Title, d), Detail: render(s.Detail, d)}
		if a := s.Action; a != nil {
			ra := *a
			ra.Label, ra.Href, ra.Text, ra.Command, ra.Confirm = render(a.Label, d), render(a.Href, d), render(a.Text, d), render(a.Command, d), render(a.Confirm, d)
			ra.Run = map[string]string{}
			for k, val := range a.Run {
				ra.Run[k] = render(val, d)
			}
			st.Action = &ra
		}
		v.Steps = append(v.Steps, st)
	}
	return v
}

// Engine watches runs and checks and keeps the issues table current.
type Engine struct {
	Root   string
	Store  *store.Store
	Runner *runner.Runner
	Checks *checks.Service
	Bus    *events.Bus
	Log    *slog.Logger

	mu       sync.Mutex
	rules    []*Rule
	ruleErrs []error
	done     map[int64]string // run id → status already processed

	// PS lists this machine's processes as "pid comm" lines; tests replace it.
	PS func() (string, error)
}

func New(root string, st *store.Store, rn *runner.Runner, ck *checks.Service, bus *events.Bus, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	e := &Engine{Root: root, Store: st, Runner: rn, Checks: ck, Bus: bus, Log: log, done: map[int64]string{}}
	e.PS = func() (string, error) {
		out, err := exec.Command("ps", "-A", "-o", "pid=,comm=").Output()
		return string(out), err
	}
	e.Reload()
	return e
}

// Reload re-reads built-in and .groundwork/rules/*.yaml rules.
func (e *Engine) Reload() {
	rs, errs := LoadRules(filepath.Join(e.Root, ".groundwork", "rules"))
	for _, err := range errs {
		e.Log.Warn("rule file skipped", "err", err)
	}
	e.mu.Lock()
	e.rules, e.ruleErrs = rs, errs
	e.mu.Unlock()
}

func (e *Engine) Rules() ([]*Rule, []error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rules, e.ruleErrs
}

func (e *Engine) rule(id string) *Rule {
	rs, _ := e.Rules()
	for _, r := range rs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// Start processes runs and findings as they change, plus a periodic sweep
// (the bus drops events for slow subscribers).
func (e *Engine) Start(ctx context.Context) {
	ch, cancel := e.Bus.Subscribe()
	go func() {
		defer cancel()
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		var debounce <-chan time.Time
		e.Sweep()
		e.ProcessDiagnostics()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				switch ev.Type {
				case "run.updated":
					var d struct {
						ID     int64  `json:"id"`
						Status string `json:"status"`
					}
					if json.Unmarshal(ev.Data, &d) == nil && terminal(d.Status) {
						e.ProcessRun(d.ID)
					}
				case "checks.updated":
					debounce = time.After(500 * time.Millisecond)
				}
			case <-debounce:
				debounce = nil
				e.ProcessDiagnostics()
			case <-tick.C:
				e.Reload() // picks up edits to .groundwork/rules
				e.Sweep()
			}
		}
	}()
}

func terminal(status string) bool {
	switch status {
	case store.StatusSucceeded, store.StatusFailed, store.StatusCancelled, store.StatusWaitingApproval:
		return true
	}
	return false
}

// Sweep processes recent runs that haven't been seen in their current status.
func (e *Engine) Sweep() {
	runs, err := e.Store.ListRuns(store.RunFilter{Limit: 50})
	if err != nil {
		return
	}
	for i := len(runs) - 1; i >= 0; i-- { // oldest first
		if terminal(runs[i].Status) {
			e.ProcessRun(runs[i].ID)
		}
	}
}

func targetKey(t runner.Target) (key, label, scope string) {
	if t.Project != "" || t.Tool == "ansible" {
		p := t.Project
		if p == "" {
			p = "."
		}
		return "ans:" + p + "#" + t.Env, fmt.Sprintf("%s (%s)", p, t.Env), "ansible"
	}
	return "tf:" + t.Root + "@" + t.Env, fmt.Sprintf("%s (%s)", t.Root, t.Env), "terraform"
}

func evidenceTime(r *store.Run) time.Time {
	switch {
	case r.EndedAt != nil:
		return *r.EndedAt
	case r.StartedAt != nil:
		return *r.StartedAt
	}
	return r.CreatedAt
}

func kindMatches(patterns []string, kind string) bool {
	for _, p := range patterns {
		if p == kind || (strings.HasSuffix(p, "*") && strings.HasPrefix(kind, strings.TrimSuffix(p, "*"))) {
			return true
		}
	}
	return false
}

func (r *Rule) resolvedBy() []string {
	if len(r.ResolvedBy) > 0 {
		return r.ResolvedBy
	}
	if r.Scope == "ansible" {
		return []string{"ans.*"}
	}
	return []string{runner.KindPlan, runner.KindDrift}
}

var (
	recapRe   = regexp.MustCompile(`(?m)^([\w.-]+)\s+: ok=\d+\s+changed=\d+\s+unreachable=(\d+)\s+failed=(\d+)`)
	successRe = regexp.MustCompile(`(?m)^([\w.-]+) \| (?:SUCCESS|CHANGED)`)
)

// hostsOK returns hosts that finished cleanly in an Ansible log.
func hostsOK(text string) map[string]bool {
	ok := map[string]bool{}
	for _, m := range recapRe.FindAllStringSubmatch(text, -1) {
		if m[2] == "0" && m[3] == "0" {
			ok[m[1]] = true
		}
	}
	for _, m := range successRe.FindAllStringSubmatch(text, -1) {
		ok[m[1]] = true
	}
	return ok
}

// ProcessRun matches a finished run's log and updates issues on its target.
func (e *Engine) ProcessRun(id int64) {
	run, err := e.Store.GetRun(id)
	if err != nil || !terminal(run.Status) {
		return
	}
	e.mu.Lock()
	if e.done[id] == run.Status {
		e.mu.Unlock()
		return
	}
	e.done[id] = run.Status
	rules := e.rules
	e.mu.Unlock()

	var t runner.Target
	_ = json.Unmarshal(run.Target, &t)
	key, label, scope := targetKey(t)
	text := e.Runner.LogText(id, 2<<20)
	var sum struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(run.Summary, &sum)
	if sum.Error != "" {
		text += "\n" + sum.Error
	}
	seen := evidenceTime(run)
	changed := false
	matched := map[string]bool{}
	for _, h := range MatchLog(rules, scope, text) {
		c := Context{Target: label, Root: t.Root, Env: t.Env, Project: t.Project, Host: h.Key, RunID: id, RunKind: run.Kind, Vars: h.Vars, Excerpt: h.Excerpt}
		if _, ch, err := e.Store.UpsertIssue(h.Rule.ID, key, h.Key, "run", seen, c); err == nil {
			changed = changed || ch
		}
		matched[h.Rule.ID+"\x00"+h.Key] = true
	}

	// resolve what this run shows is fixed
	succeeded := run.Status == store.StatusSucceeded || run.Status == store.StatusWaitingApproval
	open, _ := e.Store.OpenIssuesOn(key)
	var okHosts map[string]bool
	for _, is := range open {
		r := e.rule(is.RuleID)
		if r == nil || is.Source != "run" || matched[is.RuleID+"\x00"+is.Key] || r.Resolve == "manual" || !kindMatches(r.resolvedBy(), run.Kind) {
			continue
		}
		fixed := succeeded
		if is.Key != "" && scope == "ansible" { // per-host: did that host come through cleanly?
			if okHosts == nil {
				okHosts = hostsOK(text)
			}
			fixed = okHosts[is.Key]
		}
		if fixed {
			if ok, _ := e.Store.ResolveIssue(is.ID, seen, fmt.Sprintf("fixed: run #%d (%s) succeeded", id, run.Kind)); ok {
				changed = true
			}
		}
	}
	if changed {
		e.Bus.Publish("issues.updated", map[string]any{"run": id})
	}
}

// ProcessDiagnostics syncs issues with the current check findings.
func (e *Engine) ProcessDiagnostics() {
	if e.Checks == nil {
		return
	}
	ds, err := e.Checks.Diagnostics("", "", "")
	if err != nil {
		return
	}
	rules, _ := e.Rules()
	now := time.Now()
	changed := false
	live := map[string]bool{}
	for _, h := range MatchDiagnostics(rules, ds) {
		target := "file:" + h.Vars["file"]
		c := Context{Target: h.Vars["file"], Vars: h.Vars, Excerpt: h.Excerpt}
		if _, ch, err := e.Store.UpsertIssue(h.Rule.ID, target, h.Key, "diagnostic", now, c); err == nil {
			changed = changed || ch
		}
		live[h.Rule.ID+"\x00"+target+"\x00"+h.Key] = true
	}
	open, _ := e.Store.OpenIssuesBySource("diagnostic")
	for _, is := range open {
		if !live[is.RuleID+"\x00"+is.Target+"\x00"+is.Key] {
			if ok, _ := e.Store.ResolveIssue(is.ID, now, "fixed: the check passes now"); ok {
				changed = true
			}
		}
	}
	if changed {
		e.Bus.Publish("issues.updated", map[string]any{})
	}
}

// View renders a stored issue, running its pre-checks if it's open.
func (e *Engine) View(is store.Issue) View {
	var c Context
	_ = json.Unmarshal(is.Context, &c)
	r := e.rule(is.RuleID)
	if r == nil {
		return View{ID: is.ID, RuleID: is.RuleID, Title: is.RuleID + " (rule removed)", Status: is.Status, Target: c.Target,
			FirstSeen: is.FirstSeen, LastSeen: is.LastSeen, ResolvedAt: is.ResolvedAt, Resolution: is.Resolution, Steps: []Step{}}
	}
	v := Render(r, Hit{Rule: r, Key: is.Key, Vars: c.Vars, Excerpt: c.Excerpt}, c)
	v.ID, v.Status, v.FirstSeen, v.LastSeen, v.ResolvedAt, v.Resolution = is.ID, is.Status, is.FirstSeen, is.LastSeen, is.ResolvedAt, is.Resolution
	if v.Steps == nil {
		v.Steps = []Step{}
	}
	return v
}

// Prechecks runs a rule's automated checks for an issue.
func (e *Engine) Prechecks(is store.Issue) []CheckResult {
	r := e.rule(is.RuleID)
	if r == nil {
		return nil
	}
	var c Context
	_ = json.Unmarshal(is.Context, &c)
	var out []CheckResult
	for _, chk := range r.Checks {
		for name, arg := range chk {
			switch name {
			case "no_running_job_on":
				var ids []int64
				if c.Project != "" {
					ids = e.Runner.Running("", c.Project+"#"+c.Env)
				} else {
					ids = e.Runner.Running(c.Root, "")
				}
				if len(ids) == 0 {
					out = append(out, CheckResult{OK: true, Detail: "No groundwork job is running on " + c.Target + "."})
				} else {
					out = append(out, CheckResult{Detail: fmt.Sprintf("Run #%d is still running on %s. Wait for it or cancel it first.", ids[0], c.Target)})
				}
			case "no_local_process":
				proc := render(arg, c.data(c.Vars))
				ps, err := e.PS()
				if err != nil {
					out = append(out, CheckResult{Detail: "Couldn't list processes: " + err.Error()})
					continue
				}
				var pids []string
				for _, l := range strings.Split(ps, "\n") {
					f := strings.Fields(l)
					if len(f) >= 2 && path.Base(f[len(f)-1]) == proc {
						pids = append(pids, f[0])
					}
				}
				if len(pids) == 0 {
					out = append(out, CheckResult{OK: true, Detail: "No " + proc + " process is running on this machine."})
				} else {
					out = append(out, CheckResult{Detail: fmt.Sprintf("%s is running on this machine (pid %s). Make sure it isn't working on this state.", proc, strings.Join(pids, ", "))})
				}
			}
		}
	}
	return out
}

var ErrConfirm = errors.New("confirmation text doesn't match")

// Act performs step `step` of an issue. Only "run" actions run on the server,
// and they're built from the rule and the stored issue, never from the request.
func (e *Engine) Act(is store.Issue, step int, confirm string) (*store.Run, error) {
	v := e.View(is)
	if step < 0 || step >= len(v.Steps) || v.Steps[step].Action == nil || v.Steps[step].Action.Kind != "run" {
		return nil, fmt.Errorf("step %d has nothing to run", step+1)
	}
	if is.Status != store.IssueOpen {
		return nil, errors.New("this issue is resolved")
	}
	a := v.Steps[step].Action
	if (a.Mutating || a.Confirm != "") && confirm != a.Confirm {
		return nil, fmt.Errorf("%w: type %q", ErrConfirm, a.Confirm)
	}
	for _, ck := range e.Prechecks(is) {
		if !ck.OK && a.Mutating {
			return nil, errors.New(ck.Detail)
		}
	}
	var c Context
	_ = json.Unmarshal(is.Context, &c)
	req := runner.SubmitRequest{Kind: a.Run["kind"], Root: c.Root, Env: c.Env, Project: c.Project,
		Upgrade: a.Run["upgrade"] == "true", Limit: a.Run["limit"], LockID: a.Run["lock_id"], ConfirmText: confirm}
	if strings.HasPrefix(req.Kind, "ans.") && req.Project == "" {
		req.Project = "."
	}
	return e.Runner.Submit(req)
}
