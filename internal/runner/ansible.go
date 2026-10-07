package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rapando/groundwork/internal/ansible"
	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/files"
	"github.com/rapando/groundwork/internal/store"
)

func ansibleReadOnly(module string) bool { return ansible.ReadOnlyModules[module] }

// ResolveAnsibleTarget maps a request onto a configured project and
// environment. Every user-supplied value is validated before it can reach argv.
func ResolveAnsibleTarget(cfg *config.Config, root string, req SubmitRequest) (Target, error) {
	bad := func(f string, a ...any) (Target, error) {
		return Target{}, fmt.Errorf("%w: "+f, append([]any{ErrBadTarget}, a...)...)
	}
	if cfg == nil {
		return bad("groundwork is not configured")
	}
	var proj *config.AnsibleProject
	for i := range cfg.Ansible.Projects {
		if path.Clean(cfg.Ansible.Projects[i].Path) == path.Clean(req.Project) {
			proj = &cfg.Ansible.Projects[i]
		}
	}
	if proj == nil {
		return bad("%s is not a configured Ansible project", req.Project)
	}
	t := Target{Tool: "ansible", Project: path.Clean(proj.Path), Env: req.Env}
	if len(proj.Inventories) > 0 {
		inv, ok := proj.Inventories[req.Env]
		if !ok {
			return bad("%s has no inventory for environment %q", proj.Path, req.Env)
		}
		t.Inventory = inv
	} else {
		if req.Env != "" && req.Env != "default" {
			return bad("%s has no per-environment inventories (use env \"default\")", proj.Path)
		}
		t.Env = "default"
	}
	t.ApprovalRequired = config.IsProdLike(t.Env)
	if v := proj.VaultPasswordFile; v != "" {
		if strings.HasPrefix(v, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				v = filepath.Join(home, v[2:])
			}
		}
		t.Vault = v
	}
	for name, v := range map[string]string{"limit": req.Limit, "tags": req.Tags, "skip_tags": req.SkipTags} {
		if v != "" && !ansible.ValidPattern(v) {
			return bad("invalid %s %q", name, v)
		}
	}
	t.Limit, t.Tags, t.SkipTags = req.Limit, req.Tags, req.SkipTags

	switch req.Kind {
	case KindAnsCheck, KindAnsPlaybook:
		pb := path.Clean(req.Playbook)
		if !strings.HasSuffix(pb, ".yml") && !strings.HasSuffix(pb, ".yaml") {
			return bad("playbook must be a .yml file")
		}
		full, err := files.SafePath(filepath.Join(root, filepath.FromSlash(t.Project)), pb)
		if err != nil {
			return bad("playbook %q is outside the project", req.Playbook)
		}
		if _, err := os.Stat(full); err != nil {
			return bad("playbook %s not found in %s", pb, t.Project)
		}
		t.Playbook = pb
	case KindAnsAdhoc:
		if !ansible.ValidPattern(req.Pattern) {
			return bad("invalid host pattern %q", req.Pattern)
		}
		if !ansible.ValidModule(req.Module) {
			return bad("invalid module %q", req.Module)
		}
		if len(req.Args) > 2000 || strings.ContainsRune(req.Args, 0) {
			return bad("module arguments are too long")
		}
		t.Pattern, t.Module, t.Args = req.Pattern, req.Module, req.Args
		if !ansibleReadOnly(req.Module) && strings.TrimSpace(req.ConfirmText) != t.Env {
			return Target{}, fmt.Errorf("%w: %s can change hosts; type %q to run it", ErrConfirmText, req.Module, t.Env)
		}
	case KindAnsPing, KindAnsFacts:
		t.Pattern = "all"
		if req.Limit != "" {
			t.Pattern = req.Limit
		}
		t.Module = "ping"
		if req.Kind == KindAnsFacts {
			t.Module, t.Args = "setup", "gather_subset=min"
		}
	}
	return t, nil
}

func (r *Runner) resolveAnsible(req SubmitRequest) (Target, error) {
	return ResolveAnsibleTarget(r.Cfg(), r.Root, req)
}

func (t Target) playbookOpts(check bool) ansible.PlaybookOpts {
	return ansible.PlaybookOpts{Inventory: t.Inventory, Playbook: t.Playbook, Check: check, Limit: t.Limit, Tags: t.Tags, SkipTags: t.SkipTags, VaultPwdFile: t.Vault}
}

func (r *Runner) ansibleArgv(kind string, t Target, check bool) []string {
	switch kind {
	case KindAnsCheck, KindAnsPlaybook:
		return ansible.PlaybookArgs(t.playbookOpts(check))
	}
	return ansible.AdhocArgs(t.Inventory, t.Pattern, t.Module, t.Args, false, t.Vault)
}

// hostResult is what is persisted per task/host (hosts.ndjson) and streamed.
type hostResult struct {
	Phase string `json:"phase"` // check | run
	ansible.HostResult
}

func (r *Runner) hostsPath(id int64) string { return filepath.Join(r.runDir(id), "hosts.ndjson") }

// HostResults returns every per-host task result of a run, in order.
func (r *Runner) HostResults(id int64) ([]hostResult, error) {
	b, err := os.ReadFile(r.hostsPath(id))
	if os.IsNotExist(err) {
		return []hostResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []hostResult{}
	for _, l := range strings.Split(string(b), "\n") {
		var h hostResult
		if l != "" && json.Unmarshal([]byte(l), &h) == nil {
			out = append(out, h)
		}
	}
	return out, nil
}

// ansExec runs one ansible command for a job with the callback attached.
func (r *Runner) ansExec(j *job, stage, phase string, argv []string) (execResult, map[string]ansible.HostStats, error) {
	cbEnv, err := ansible.InstallCallback(filepath.Join(r.runDir(j.id), "callback"))
	if err != nil {
		return execResult{}, nil, err
	}
	home := filepath.Join(r.Root, ".groundwork", "ansible-home")
	_ = os.MkdirAll(home, 0o755)
	env := append(cbEnv, "ANSIBLE_HOME="+home, "ANSIBLE_NOCOLOR=1", "ANSIBLE_FORCE_COLOR=0", "PYTHONUNBUFFERED=1", "ANSIBLE_RETRY_FILES_ENABLED=False")

	var mu sync.Mutex
	var stats map[string]ansible.HostStats
	hf, err := os.OpenFile(r.hostsPath(j.id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return execResult{}, nil, err
	}
	defer hf.Close()
	scope := j.target.Scope()
	onEvent := func(line []byte) {
		ev, ok := ansible.ParseEvent(line)
		if !ok {
			return
		}
		switch {
		case ev.Result != nil:
			res := *ev.Result
			res.Msg, res.Diff = j.redactMulti(res.Msg), j.redactMulti(res.Diff)
			hr := hostResult{Phase: phase, HostResult: res}
			if b, err := json.Marshal(hr); err == nil {
				mu.Lock()
				hf.Write(append(b, '\n'))
				mu.Unlock()
			}
			r.Bus.Publish("ans.result", map[string]any{"run": j.id, "result": hr})
			r.recordHost(scope, j.target.Module, res)
		case ev.Stats != nil:
			mu.Lock()
			stats = ev.Stats
			mu.Unlock()
		}
	}
	res, err := r.execute(j, execOpts{stage: stage, argv: argv, dir: r.abs(j.target.Project), env: env, events: onEvent})
	mu.Lock()
	defer mu.Unlock()
	return res, stats, err
}

// redactMulti redacts each line of a multi-line message or diff.
func (j *job) redactMulti(s string) string {
	if s == "" || j.red == nil {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = j.red.Line(l)
	}
	return strings.Join(lines, "\n")
}

// recordHost updates reachability and the facts cache from ping/setup results.
func (r *Runner) recordHost(scope, module string, res ansible.HostResult) {
	now := time.Now()
	switch res.Status {
	case "unreachable":
		_ = r.Store.SetHostStatus(scope, store.HostStatus{Host: res.Host, Reachable: false, Msg: res.Msg, CheckedAt: now})
	case "ok", "changed":
		if module == "ping" || module == "setup" {
			_ = r.Store.SetHostStatus(scope, store.HostStatus{Host: res.Host, Reachable: true, LatencyMS: int(res.Duration * 1000), CheckedAt: now})
		}
	}
	if len(res.Facts) > 0 {
		if b, err := json.Marshal(res.Facts); err == nil {
			_ = r.Store.SetFacts(scope, res.Host, string(b))
		}
	}
}

// hostFailures: ansible-playbook exits 2 (host failed), 3/4 (unreachable) when it
// ran to completion but some hosts didn't succeed; anything else is an error.
func hostFailures(code int) bool { return code == 2 || code == 3 || code == 4 }

func failedHosts(stats map[string]ansible.HostStats) string {
	var failed, unreach []string
	for h, s := range stats {
		if s.Failures > 0 {
			failed = append(failed, h)
		}
		if s.Unreachable > 0 {
			unreach = append(unreach, h)
		}
	}
	sort.Strings(failed)
	sort.Strings(unreach)
	var parts []string
	if len(failed) > 0 {
		parts = append(parts, fmt.Sprintf("%d host(s) failed: %s", len(failed), strings.Join(failed, ", ")))
	}
	if len(unreach) > 0 {
		parts = append(parts, fmt.Sprintf("%d unreachable: %s", len(unreach), strings.Join(unreach, ", ")))
	}
	return strings.Join(parts, "; ")
}

func (r *Runner) ansPrepare(j *job, stages []string) bool {
	if err := r.begin(j); err != nil {
		r.end(j, store.StatusFailed, nil, err.Error())
		return false
	}
	for _, n := range stages {
		_ = r.Store.UpsertStage(store.RunStage{RunID: j.id, Name: n, Status: "pending"})
	}
	if !r.acquire(j) {
		r.end(j, store.StatusCancelled, nil, "")
		return false
	}
	start := time.Now()
	_ = r.Store.UpdateRun(j.id, store.RunUpdate{Status: store.StatusRunning, Started: &start})
	r.publishRun(j.id)
	return true
}

func (r *Runner) ansSyntax(j *job) error {
	return r.stage(j, "syntax", func() error {
		res, err := r.execute(j, execOpts{stage: "syntax", argv: ansible.SyntaxArgs(j.target.playbookOpts(false)), dir: r.abs(j.target.Project),
			env: []string{"ANSIBLE_HOME=" + filepath.Join(r.Root, ".groundwork", "ansible-home"), "ANSIBLE_NOCOLOR=1"}})
		if err != nil {
			return err
		}
		if res.exit != 0 {
			return fmt.Errorf("syntax check failed (exit %d)", res.exit)
		}
		return nil
	})
}

// ansPlaybookStage runs ansible-playbook (check or for real) as a stage and
// stores per-host totals. Host failures fail the stage only if strict.
func (r *Runner) ansPlaybookStage(j *job, name, phase string, check, strict bool) (map[string]ansible.HostStats, int, error) {
	var stats map[string]ansible.HostStats
	var code int
	err := r.stage(j, name, func() error {
		res, st, err := r.ansExec(j, name, phase, ansible.PlaybookArgs(j.target.playbookOpts(check)))
		stats, code = st, res.exit
		if err != nil {
			return err
		}
		switch {
		case res.exit == 0:
			return nil
		case hostFailures(res.exit) && !strict:
			return nil
		case hostFailures(res.exit):
			return fmt.Errorf("%s", failedHosts(st))
		}
		return fmt.Errorf("ansible-playbook failed (exit %d)", res.exit)
	})
	r.saveSummary(j.id, func(s *Summary) {
		if s.Ansible == nil {
			s.Ansible = &AnsibleSummary{}
		}
		if phase == "check" {
			s.Ansible.Check = stats
		} else {
			s.Ansible.Run = stats
		}
	})
	return stats, code, err
}

// runAnsCheck: a dry run (--check --diff). Read-only; no approval.
func (r *Runner) runAnsCheck(j *job) {
	defer r.finish(j)
	if !r.ansPrepare(j, []string{"syntax", "check"}) {
		return
	}
	defer r.release(j)
	if err := r.ansSyntax(j); err != nil {
		r.failure(j, err, 1)
		return
	}
	stats, code, err := r.ansPlaybookStage(j, "check", "check", true, true)
	if err != nil {
		r.failure(j, err, code)
		return
	}
	_ = stats
	r.end(j, store.StatusSucceeded, &code, "")
}

// runAnsPlaybookPhase: syntax → dry run, then wait for approval. A dry run
// with host failures can still be approved (check mode often fails on tasks
// that depend on earlier changes); the failures are shown for review.
func (r *Runner) runAnsPlaybookPhase(j *job) {
	defer r.finish(j)
	if !r.ansPrepare(j, []string{"syntax", "check", "approve", "run"}) {
		return
	}
	locked := true
	defer func() {
		if locked {
			r.release(j)
		}
	}()
	if err := r.ansSyntax(j); err != nil {
		r.failure(j, err, 1)
		return
	}
	if _, code, err := r.ansPlaybookStage(j, "check", "check", true, false); err != nil {
		r.failure(j, err, code)
		return
	}
	fp, err := r.fingerprint(j.target)
	if err != nil {
		r.failure(j, err, 1)
		return
	}
	planned := time.Now().UTC()
	r.saveSummary(j.id, func(s *Summary) { s.Fingerprint, s.PlannedAt = fp, &planned })
	j.emit("check", "info", "Dry run finished. Waiting for approval.", nil)
	now := time.Now()
	approve := "approve"
	_ = r.Store.UpsertStage(store.RunStage{RunID: j.id, Name: "approve", Status: "running", StartedAt: &now, Detail: "waiting for approval"})
	_ = r.Store.UpdateRun(j.id, store.RunUpdate{Status: store.StatusWaitingApproval, Stage: &approve})
	r.release(j)
	locked = false
}

// runAnsRunPhase: the real run, after approval, re-checking freshness under the lock.
func (r *Runner) runAnsRunPhase(j *job) {
	defer r.finish(j)
	if err := r.begin(j); err != nil {
		r.end(j, store.StatusFailed, nil, err.Error())
		return
	}
	if !r.acquire(j) {
		r.end(j, store.StatusCancelled, nil, "")
		return
	}
	defer r.release(j)
	_ = r.Store.UpdateRun(j.id, store.RunUpdate{Status: store.StatusRunning})
	r.publishRun(j.id)
	run, err := r.Store.GetRun(j.id)
	if err == nil {
		if reasons, _, serr := r.staleness(j.ctx, j.target, readSummary(run), false); serr == nil && len(reasons) > 0 {
			now := time.Now()
			_ = r.Store.UpsertStage(store.RunStage{RunID: j.id, Name: "run", Status: "failed", EndedAt: &now, Detail: "stale"})
			r.failure(j, &StaleError{Reasons: reasons}, 1)
			return
		}
	}
	_, code, err := r.ansPlaybookStage(j, "run", "run", false, true)
	if err != nil {
		r.failure(j, err, code)
		return
	}
	r.end(j, store.StatusSucceeded, &code, "")
}

// runAnsAdhoc: ad-hoc command, ping or facts.
func (r *Runner) runAnsAdhoc(j *job) {
	defer r.finish(j)
	name := map[string]string{KindAnsPing: "ping", KindAnsFacts: "facts"}[j.kind]
	if name == "" {
		name = "run"
	}
	if !r.ansPrepare(j, []string{name}) {
		return
	}
	defer r.release(j)
	var stats map[string]ansible.HostStats
	var code int
	err := r.stage(j, name, func() error {
		t := j.target
		res, st, err := r.ansExec(j, name, "run", ansible.AdhocArgs(t.Inventory, t.Pattern, t.Module, t.Args, false, t.Vault))
		stats, code = st, res.exit
		if err != nil {
			return err
		}
		// ping/facts report unreachable hosts as data, not as a failed job
		if res.exit == 0 || (hostFailures(res.exit) && j.kind != KindAnsAdhoc) {
			return nil
		}
		if hostFailures(res.exit) {
			return fmt.Errorf("%s", failedHosts(st))
		}
		return fmt.Errorf("ansible failed (exit %d)", res.exit)
	})
	r.saveSummary(j.id, func(s *Summary) { s.Ansible = &AnsibleSummary{Run: stats} })
	if err != nil {
		r.failure(j, err, code)
		return
	}
	r.end(j, store.StatusSucceeded, &code, "")
}
