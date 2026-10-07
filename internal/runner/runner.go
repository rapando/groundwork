// Package runner executes Terraform jobs: queueing, per-root locking, process
// management, log capture and the plan → approve → apply pipeline.
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rapando/groundwork/internal/ansible"
	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/redact"
	"github.com/rapando/groundwork/internal/store"
	"github.com/rapando/groundwork/internal/terraform"
)

const (
	KindInit = "tf.init"
	KindPlan = "tf.plan" // init → validate → plan → approve → apply

	KindDrift = "tf.drift" // init → refresh-only plan: compare state with real infrastructure

	KindUnlock = "tf.unlock" // force-unlock a stale state lock; confirmed by typing the env

	KindAnsCheck    = "ans.check"    // syntax → check (dry run)
	KindAnsPlaybook = "ans.playbook" // syntax → check → approve → run
	KindAnsAdhoc    = "ans.adhoc"
	KindAnsPing     = "ans.ping"
	KindAnsFacts    = "ans.facts"
)

var (
	ErrNeedsAck      = errors.New("this plan destroys or replaces data-bearing resources; acknowledge it to approve")
	ErrNotApprovable = errors.New("run is not waiting for approval")
	ErrConfirmText   = errors.New("confirmation text does not match")
	ErrPlanChanged   = errors.New("the saved plan file changed since it was created")
	ErrNotCancelable = errors.New("run has already finished")
	ErrBadTarget     = errors.New("invalid target")
)

// Target is what a job runs against, resolved from groundwork.yaml at submit
// time and persisted with the run.
type Target struct {
	Tool             string   `json:"tool,omitempty"` // terraform (default) | ansible
	Root             string   `json:"root,omitempty"` // terraform root, repo-relative
	Env              string   `json:"env"`
	Workspace        string   `json:"workspace,omitempty"`
	VarFiles         []string `json:"var_files,omitempty"` // repo-relative
	ApprovalRequired bool     `json:"approval_required"`
	Binary           string   `json:"binary,omitempty"`
	Upgrade          bool     `json:"upgrade,omitempty"`

	// Ansible
	Project   string `json:"project,omitempty"`   // repo-relative project dir
	Inventory string `json:"inventory,omitempty"` // relative to the project; "" = ansible.cfg default
	Playbook  string `json:"playbook,omitempty"`  // relative to the project
	Limit     string `json:"limit,omitempty"`
	Tags      string `json:"tags,omitempty"`
	SkipTags  string `json:"skip_tags,omitempty"`
	Pattern   string `json:"pattern,omitempty"` // ad-hoc host pattern
	Module    string `json:"module,omitempty"`
	Args      string `json:"args,omitempty"`
	Vault     string `json:"vault_password_file,omitempty"` // absolute path, from config
}

func (t Target) isAnsible() bool { return t.Tool == "ansible" }

// Scope identifies an ansible project + environment (facts, reachability).
func (t Target) Scope() string { return t.Project + "#" + t.Env }

// SubmitRequest starts a job. Terraform jobs use Root; Ansible jobs use Project.
type SubmitRequest struct {
	Kind        string `json:"kind"`
	Root        string `json:"root"`
	Project     string `json:"project"`
	Env         string `json:"env"`
	Upgrade     bool   `json:"upgrade"`
	Playbook    string `json:"playbook"`
	Limit       string `json:"limit"`
	Tags        string `json:"tags"`
	SkipTags    string `json:"skip_tags"`
	Pattern     string `json:"pattern"`
	Module      string `json:"module"`
	Args        string `json:"args"`
	ConfirmText string `json:"confirm_text"`
	LockID      string `json:"lock_id"` // tf.unlock
}

var lockIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ResolveTarget maps (root, env) onto the configuration. Nothing the caller
// passes is used as a path or argument unless it matches the config.
func ResolveTarget(cfg *config.Config, root, env string) (Target, error) {
	if cfg == nil {
		return Target{}, fmt.Errorf("%w: groundwork is not configured", ErrBadTarget)
	}
	root = path.Clean(root)
	for _, r := range cfg.Terraform.Roots {
		if path.Clean(r.Path) != root {
			continue
		}
		t := Target{Root: root, Binary: cfg.Terraform.Binary, ApprovalRequired: r.ApprovalRequired(env)}
		if t.Binary == "" {
			t.Binary = "terraform"
		}
		if r.Env != "" {
			if env != "" && env != r.Env {
				return Target{}, fmt.Errorf("%w: %s is the %q environment, not %q", ErrBadTarget, root, r.Env, env)
			}
			t.Env = r.Env
			t.ApprovalRequired = r.ApprovalRequired(r.Env)
			return t, nil
		}
		e, ok := r.Envs[env]
		if !ok {
			return Target{}, fmt.Errorf("%w: %s has no environment %q", ErrBadTarget, root, env)
		}
		t.Env, t.Workspace = env, e.Workspace
		if t.Workspace == "" {
			t.Workspace = env
		}
		for _, vf := range e.VarFiles {
			t.VarFiles = append(t.VarFiles, path.Join(root, vf))
		}
		return t, nil
	}
	return Target{}, fmt.Errorf("%w: %s is not a configured Terraform root", ErrBadTarget, root)
}

// Summary is stored in runs.summary_json.
type Summary struct {
	Plan        *terraform.PlanSummary `json:"plan,omitempty"`
	PlanSHA256  string                 `json:"plan_sha256,omitempty"`
	NoChanges   bool                   `json:"no_changes,omitempty"`
	Danger      []terraform.Danger     `json:"danger,omitempty"`
	State       *StateRef              `json:"state,omitempty"` // the state the plan was made against
	Fingerprint string                 `json:"fingerprint,omitempty"`
	PlannedAt   *time.Time             `json:"planned_at,omitempty"`
	Apply       *ApplySummary          `json:"apply,omitempty"`
	Ansible     *AnsibleSummary        `json:"ansible,omitempty"`
	Drift       *DriftSummary          `json:"drift,omitempty"`
	Error       string                 `json:"error,omitempty"`
}

// DriftSummary counts what a refresh-only plan found.
type DriftSummary struct {
	Resources int `json:"resources"`
	Attrs     int `json:"attrs"`
}

// AnsibleSummary holds per-host totals of the dry run and of the real run.
type AnsibleSummary struct {
	Check map[string]ansible.HostStats `json:"check,omitempty"`
	Run   map[string]ansible.HostStats `json:"run,omitempty"`
}

type ApplySummary struct {
	Added     int      `json:"added"`
	Changed   int      `json:"changed"`
	Destroyed int      `json:"destroyed"`
	Outputs   []string `json:"outputs,omitempty"`
}

// Detail is everything the UI needs to render a run.
type Detail struct {
	Run      *store.Run       `json:"run"`
	Target   Target           `json:"target"`
	Stages   []store.RunStage `json:"stages"`
	Summary  Summary          `json:"summary"`
	Approval *store.Approval  `json:"approval,omitempty"`
}

type Runner struct {
	Root      string
	Store     *store.Store
	Bus       *events.Bus
	Log       *slog.Logger
	Cfg       func() *config.Config
	KillGrace time.Duration // SIGINT → SIGKILL (default 15s)

	mu    sync.Mutex
	jobs  map[int64]*job
	locks map[string]chan struct{}
	wg    sync.WaitGroup
}

func New(root string, st *store.Store, bus *events.Bus, cfg func() *config.Config, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{Root: root, Store: st, Bus: bus, Cfg: cfg, Log: log, KillGrace: 15 * time.Second,
		jobs: map[int64]*job{}, locks: map[string]chan struct{}{}}
}

func (r *Runner) runDir(id int64) string {
	return filepath.Join(r.Root, ".groundwork", "runs", strconv.FormatInt(id, 10))
}
func (r *Runner) logPath(id int64) string  { return filepath.Join(r.runDir(id), "log.ndjson") }
func (r *Runner) planPath(id int64) string { return filepath.Join(r.runDir(id), "tfplan") }

// LogFile / Resources expose the run's log to the API layer.
func (r *Runner) ReadLog(id int64, from, limit int, level, q string) (*LogPage, error) {
	return ReadLog(r.logPath(id), from, limit, level, q)
}
func (r *Runner) Resources(id int64) ([]terraform.ResEvent, error) {
	return ReadResources(r.logPath(id))
}

type job struct {
	id     int64
	kind   string
	target Target
	ctx    context.Context
	cancel context.CancelFunc
	log    *logWriter
	red    *redact.Redactor

	mu          sync.Mutex // guards lastErr and apply: stdout and stderr readers both write them
	lastErr     string     // first diagnostic ("Error: ..."): the root cause
	fallbackErr string     // first error-level line of any kind, used if there is no diagnostic
	apply       *ApplySummary
	cancelled   bool
}

func (j *job) errText() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.lastErr != "" {
		return j.lastErr
	}
	return j.fallbackErr
}

func (j *job) applySummary() *ApplySummary {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.apply == nil {
		return nil
	}
	a := *j.apply
	return &a
}

// Submit validates, records and starts a job. It returns as soon as the run
// exists; progress arrives through events.
func (r *Runner) Submit(req SubmitRequest) (*store.Run, error) {
	var (
		t    Target
		argv []string
		err  error
	)
	switch req.Kind {
	case KindInit, KindPlan, KindDrift, KindUnlock:
		t, err = ResolveTarget(r.Cfg(), req.Root, req.Env)
		if err != nil {
			return nil, err
		}
		t.Upgrade = req.Upgrade
		argv = terraform.InitArgs(t.Binary, r.abs(t.Root), req.Upgrade)
		switch req.Kind {
		case KindUnlock:
			if !lockIDRe.MatchString(req.LockID) {
				return nil, fmt.Errorf("%w: invalid lock ID", ErrBadTarget)
			}
			if req.ConfirmText != t.Env {
				return nil, fmt.Errorf("%w: type %q to confirm the force-unlock", ErrBadTarget, t.Env)
			}
			argv = terraform.ForceUnlockArgs(t.Binary, r.abs(t.Root), req.LockID)
		case KindPlan:
			argv = terraform.PlanArgs(t.Binary, r.abs(t.Root), "<run>/tfplan", r.absAll(t.VarFiles))
		case KindDrift:
			argv = terraform.DriftArgs(t.Binary, r.abs(t.Root), "<run>/drift.tfplan", r.absAll(t.VarFiles))
		}
	case KindAnsCheck, KindAnsPlaybook, KindAnsAdhoc, KindAnsPing, KindAnsFacts:
		t, err = r.resolveAnsible(req)
		if err != nil {
			return nil, err
		}
		argv = r.ansibleArgv(req.Kind, t, req.Kind == KindAnsCheck || req.Kind == KindAnsPlaybook)
	default:
		return nil, fmt.Errorf("%w: unknown kind %q", ErrBadTarget, req.Kind)
	}
	run, err := r.Store.CreateRun(req.Kind, t, argv, r.gitHead())
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(r.runDir(run.ID), 0o700); err != nil {
		return nil, err
	}
	if req.Kind == KindAnsAdhoc && !ansibleReadOnly(t.Module) {
		// a mutating ad-hoc command was confirmed at submit time: that is its approval
		now := time.Now()
		_ = r.Store.SaveApproval(store.Approval{RunID: run.ID, ApprovedBy: currentUser(), ApprovedAt: now, ConfirmText: req.ConfirmText})
	}
	j := r.newJob(run.ID, req.Kind, t)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		switch req.Kind {
		case KindInit:
			r.runInit(j)
		case KindPlan:
			r.runPlanPhase(j)
		case KindDrift:
			r.runDrift(j)
		case KindUnlock:
			r.runUnlock(j, argv)
		case KindAnsCheck:
			r.runAnsCheck(j)
		case KindAnsPlaybook:
			r.runAnsPlaybookPhase(j)
		default:
			r.runAnsAdhoc(j)
		}
	}()
	r.publishRun(run.ID)
	return run, nil
}

func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "unknown"
}

func (r *Runner) newJob(id int64, kind string, t Target) *job {
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{id: id, kind: kind, target: t, ctx: ctx, cancel: cancel}
	r.mu.Lock()
	r.jobs[id] = j
	r.mu.Unlock()
	return j
}

func (r *Runner) abs(rel string) string { return filepath.Join(r.Root, filepath.FromSlash(rel)) }
func (r *Runner) absAll(rels []string) []string {
	out := make([]string, len(rels))
	for i, x := range rels {
		out[i] = r.abs(x)
	}
	return out
}

func (r *Runner) gitHead() string {
	out, err := exec.Command("git", "-C", r.Root, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Cancel stops a running or queued job (SIGINT first, so terraform can release
// its state lock) or discards a plan that is waiting for approval.
func (r *Runner) Cancel(id int64) error {
	run, err := r.Store.GetRun(id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	j := r.jobs[id]
	r.mu.Unlock()
	switch {
	case j != nil:
		j.cancelled = true
		j.cancel()
		return nil
	case run.Status == store.StatusWaitingApproval:
		now := time.Now()
		_ = r.Store.UpsertStage(store.RunStage{RunID: id, Name: "approve", Status: "cancelled", EndedAt: &now})
		_ = r.Store.UpsertStage(store.RunStage{RunID: id, Name: "apply", Status: "skipped", EndedAt: &now})
		_ = r.Store.UpdateRun(id, store.RunUpdate{Status: store.StatusCancelled, Ended: &now})
		r.removePlan(id)
		r.publishRun(id)
		return nil
	}
	return ErrNotCancelable
}

// ApproveRequest is what the user submits on the plan screen.
type ApproveRequest struct {
	ConfirmText string `json:"confirm_text"`
	AckDanger   bool   `json:"acknowledge_danger"`
}

// Approve records the approval and starts the apply of a waiting plan. The
// apply uses the saved plan file, so exactly what was reviewed is what runs.
// It is refused when the plan file was altered, the plan is older than the
// configured TTL, the configuration or the state changed since the plan, a
// typed confirmation is missing, or dangerous changes aren't acknowledged.
// The apply stage re-checks freshness under the root lock right before it runs.
func (r *Runner) Approve(id int64, req ApproveRequest) error {
	r.mu.Lock()
	if r.jobs[id] != nil { // an apply for this run is already in flight
		r.mu.Unlock()
		return ErrNotApprovable
	}
	run, err := r.Store.GetRun(id)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	if (run.Kind != KindPlan && run.Kind != KindAnsPlaybook) || run.Status != store.StatusWaitingApproval {
		r.mu.Unlock()
		return ErrNotApprovable
	}
	var t Target
	_ = json.Unmarshal(run.Target, &t)
	sum := readSummary(run)
	if t.ApprovalRequired && strings.TrimSpace(req.ConfirmText) != t.Env {
		r.mu.Unlock()
		return fmt.Errorf("%w: type %q to approve", ErrConfirmText, t.Env)
	}
	if len(sum.Danger) > 0 && !req.AckDanger {
		r.mu.Unlock()
		return ErrNeedsAck
	}
	j := r.newJobLocked(id, run.Kind, t) // claim the run: a concurrent Approve now sees a job and is refused
	r.mu.Unlock()

	fail := func(err error) error { r.dropJob(j); return err }
	sha := ""
	if !t.isAnsible() { // a playbook has no plan file: the dry run plus freshness is the contract
		sha, err = fileSHA(r.planPath(id))
		if err != nil {
			return fail(fmt.Errorf("saved plan is missing: %w", err))
		}
		if sum.PlanSHA256 != "" && sum.PlanSHA256 != sha {
			return fail(ErrPlanChanged)
		}
	}
	reasons, cur, err := r.staleness(context.Background(), t, sum, true)
	if err != nil {
		return fail(&StaleError{Reasons: []string{"could not verify that the state is unchanged: " + err.Error()}})
	}
	if len(reasons) > 0 {
		return fail(&StaleError{Reasons: reasons})
	}

	now := time.Now()
	who := "unknown"
	if u, err := user.Current(); err == nil {
		who = u.Username
	}
	if err := r.Store.SaveApproval(store.Approval{RunID: id, PlanSHA256: sha, StateSerial: cur.Serial, ApprovedBy: who,
		ApprovedAt: now, ConfirmText: req.ConfirmText, AckDanger: req.AckDanger}); err != nil {
		return fail(err)
	}
	_ = r.Store.UpsertStage(store.RunStage{RunID: id, Name: "approve", Status: "succeeded", EndedAt: &now, Detail: "approved by " + who})
	_ = r.Store.UpdateRun(id, store.RunUpdate{Status: store.StatusQueued})
	r.publishRun(id)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		if t.isAnsible() {
			r.runAnsRunPhase(j)
		} else {
			r.runApplyPhase(j)
		}
	}()
	return nil
}

// Freshness reports, cheaply, whether a waiting plan is still approvable (age
// and configuration). The state is only read at approval time.
func (r *Runner) Freshness(id int64) ([]string, error) {
	run, err := r.Store.GetRun(id)
	if err != nil {
		return nil, err
	}
	var t Target
	_ = json.Unmarshal(run.Target, &t)
	reasons, _, err := r.staleness(context.Background(), t, readSummary(run), false)
	return reasons, err
}

// Replan discards a waiting plan (if any) and starts a fresh one for the same target.
func (r *Runner) Replan(id int64) (*store.Run, error) {
	run, err := r.Store.GetRun(id)
	if err != nil {
		return nil, err
	}
	var t Target
	_ = json.Unmarshal(run.Target, &t)
	if run.Status == store.StatusWaitingApproval {
		if err := r.Cancel(id); err != nil {
			return nil, err
		}
	}
	if t.isAnsible() {
		return r.Submit(SubmitRequest{Kind: KindAnsPlaybook, Project: t.Project, Env: t.Env, Playbook: t.Playbook, Limit: t.Limit, Tags: t.Tags, SkipTags: t.SkipTags})
	}
	return r.Submit(SubmitRequest{Kind: KindPlan, Root: t.Root, Env: t.Env})
}

// ResourceDiff returns the masked attribute diff of one resource in a run's plan.
func (r *Runner) ResourceDiff(id int64, address string) (*terraform.ResourceDiff, *terraform.Location, error) {
	b, err := os.ReadFile(filepath.Join(r.runDir(id), "diff.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("no saved diff for run %d", id)
	}
	var diffs map[string]*terraform.ResourceDiff
	if err := json.Unmarshal(b, &diffs); err != nil {
		return nil, nil, err
	}
	d, ok := diffs[address]
	if !ok {
		return nil, nil, fmt.Errorf("%s is not in this plan", address)
	}
	run, err := r.Store.GetRun(id)
	if err != nil {
		return nil, nil, err
	}
	var t Target
	_ = json.Unmarshal(run.Target, &t)
	return d, terraform.Locate(r.Root, r.abs(t.Root), address), nil
}

func (r *Runner) newJobLocked(id int64, kind string, t Target) *job {
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{id: id, kind: kind, target: t, ctx: ctx, cancel: cancel}
	r.jobs[id] = j
	return j
}

func (r *Runner) dropJob(j *job) {
	r.mu.Lock()
	delete(r.jobs, j.id)
	r.mu.Unlock()
}

// Detail assembles a run for the API.
func (r *Runner) Detail(id int64) (*Detail, error) {
	run, err := r.Store.GetRun(id)
	if err != nil {
		return nil, err
	}
	st, err := r.Store.ListStages(id)
	if err != nil {
		return nil, err
	}
	d := &Detail{Run: run, Stages: st, Summary: readSummary(run)}
	_ = json.Unmarshal(run.Target, &d.Target)
	d.Approval, _ = r.Store.GetApproval(id)
	return d, nil
}

func readSummary(run *store.Run) Summary {
	var s Summary
	_ = json.Unmarshal(run.Summary, &s)
	return s
}

func fileSHA(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (r *Runner) removePlan(id int64) {
	_ = os.Remove(r.planPath(id))
	_ = os.Remove(filepath.Join(r.runDir(id), "plan.json"))
}

func (r *Runner) publishRun(id int64) {
	run, err := r.Store.GetRun(id)
	if err != nil {
		return
	}
	r.Bus.Publish("run.updated", map[string]any{"id": id, "kind": run.Kind, "status": run.Status, "stage": run.Stage})
}

// Wait blocks until all in-flight jobs finish (tests, graceful shutdown).
func (r *Runner) Wait() { r.wg.Wait() }

// Active reports runs that are queued or running.
func (r *Runner) Active() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.jobs)
}

// Shutdown cancels every job (terraform gets SIGINT) and waits for them.
func (r *Runner) Shutdown(ctx context.Context) {
	r.mu.Lock()
	for _, j := range r.jobs {
		j.cancelled = true
		j.cancel()
	}
	r.mu.Unlock()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Recover runs once at startup: runs that were in flight when the process died
// are marked failed, and an interrupted apply raises a "possible stale lock"
// issue for its root. Old runs are pruned.
func (r *Runner) Recover(keep int, maxAge time.Duration) error {
	runs, err := r.Store.InterruptedRuns()
	if err != nil {
		return err
	}
	for _, run := range runs {
		var t Target
		_ = json.Unmarshal(run.Target, &t)
		if run.Stage == "apply" {
			_ = r.Store.RaiseIssue("tf-state-lock-possible", t.Root, map[string]any{"run": run.ID, "env": t.Env, "kind": run.Kind})
		}
	}
	ids, err := r.Store.PruneRuns(keep, maxAge)
	if err != nil {
		return err
	}
	for _, id := range ids {
		_ = os.RemoveAll(r.runDir(id))
	}
	return nil
}
