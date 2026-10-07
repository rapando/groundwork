// Package drift runs refresh-only drift detection on a schedule and tells the
// user when something new drifted.
package drift

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/runner"
	"github.com/rapando/groundwork/internal/store"
)

// Notifier delivers one message. Tests replace it.
type Notifier func(ctx context.Context, d config.Drift, n Notice) error

// Notice is what gets sent when new drift appears.
type Notice struct {
	Text      string   `json:"text"` // Slack/Teams-compatible summary
	Root      string   `json:"root"`
	Env       string   `json:"env"`
	Count     int      `json:"drift_count"`
	Addresses []string `json:"addresses"`
	RunID     int64    `json:"run_id"`
	URL       string   `json:"url,omitempty"`
}

type Status struct {
	Schedule string     `json:"schedule,omitempty"`
	Notify   string     `json:"notify,omitempty"`
	Next     *time.Time `json:"next,omitempty"`
	LastRun  *time.Time `json:"last_run,omitempty"`
	Error    string     `json:"error,omitempty"`
}

type Scheduler struct {
	Runner *runner.Runner
	Store  *store.Store
	Cfg    func() *config.Config
	Log    *slog.Logger
	Notify Notifier
	URL    func() string // base URL of the UI, for links in notices

	mu      sync.Mutex
	cron    *cron.Cron
	spec    string
	entry   cron.EntryID
	lastRun *time.Time
	err     string
}

func New(rn *runner.Runner, st *store.Store, cfg func() *config.Config, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	s := &Scheduler{Runner: rn, Store: st, Cfg: cfg, Log: log, Notify: Send, cron: cron.New()}
	s.cron.Start()
	return s
}

// Apply (re)installs the schedule from the current config. Call it on start
// and whenever groundwork.yaml changes.
func (s *Scheduler) Apply() {
	cfg := s.Cfg()
	spec := ""
	if cfg != nil {
		spec = strings.TrimSpace(cfg.Drift.Schedule)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if spec == s.spec {
		return
	}
	if s.entry != 0 {
		s.cron.Remove(s.entry)
		s.entry = 0
	}
	s.spec, s.err = spec, ""
	if spec == "" {
		return
	}
	id, err := s.cron.AddFunc(spec, func() { s.RunAll(context.Background()) })
	if err != nil {
		s.err = "invalid schedule: " + err.Error()
		return
	}
	s.entry = id
}

func (s *Scheduler) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Schedule: s.spec, LastRun: s.lastRun, Error: s.err}
	if cfg := s.Cfg(); cfg != nil {
		st.Notify = cfg.Drift.Notify
	}
	if s.entry != 0 {
		n := s.cron.Entry(s.entry).Next
		if !n.IsZero() {
			st.Next = &n
		}
	}
	return st
}

func (s *Scheduler) Stop() { <-s.cron.Stop().Done() }

type target struct{ root, env string }

func targets(cfg *config.Config) []target {
	var out []target
	for _, r := range cfg.Terraform.Roots {
		if r.Env != "" {
			out = append(out, target{r.Path, r.Env})
			continue
		}
		var envs []string
		for e := range r.Envs {
			envs = append(envs, e)
		}
		sort.Strings(envs)
		for _, e := range envs {
			out = append(out, target{r.Path, e})
		}
	}
	return out
}

// RunAll checks every Terraform target for drift, one at a time (each run
// still queues behind other jobs on its root), and notifies about new drift.
func (s *Scheduler) RunAll(ctx context.Context) {
	cfg := s.Cfg()
	if cfg == nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	s.lastRun = &now
	s.mu.Unlock()
	for _, t := range targets(cfg) {
		if ctx.Err() != nil {
			return
		}
		run, err := s.Runner.Submit(runner.SubmitRequest{Kind: runner.KindDrift, Root: t.root, Env: t.env})
		if err != nil {
			s.Log.Warn("scheduled drift", "root", t.root, "env", t.env, "err", err)
			continue
		}
		status := s.wait(ctx, run.ID)
		if status != store.StatusSucceeded {
			continue // a failed check opens an issue through the diagnostics engine
		}
		s.check(ctx, cfg, t, run.ID)
	}
}

func (s *Scheduler) wait(ctx context.Context, id int64) string {
	for {
		run, err := s.Store.GetRun(id)
		if err != nil {
			return ""
		}
		switch run.Status {
		case store.StatusSucceeded, store.StatusFailed, store.StatusCancelled:
			return run.Status
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// check notifies when the set of drifted addresses differs from the last notice.
func (s *Scheduler) check(ctx context.Context, cfg *config.Config, t target, runID int64) {
	rows, err := s.Store.ListDrift(t.root, t.env)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	var addrs []string
	for _, r := range rows {
		if !seen[r.Address] {
			seen[r.Address] = true
			addrs = append(addrs, r.Address)
		}
	}
	sort.Strings(addrs)
	sum := sha256.Sum256([]byte(strings.Join(addrs, "\n")))
	key := "drift.notified." + t.root + "@" + t.env
	prev, _, _ := s.Store.GetKV(key)
	cur := hex.EncodeToString(sum[:])
	if len(addrs) == 0 {
		_ = s.Store.SetKV(key, "")
		return
	}
	if prev == cur || cfg.Drift.Notify == "none" {
		return
	}
	n := Notice{Root: t.root, Env: t.env, Count: len(addrs), Addresses: addrs, RunID: runID}
	n.Text = fmt.Sprintf("Drift in %s (%s): %d resource%s changed outside Terraform: %s", t.env, t.root, len(addrs), map[bool]string{true: "", false: "s"}[len(addrs) == 1], strings.Join(addrs[:min(len(addrs), 5)], ", "))
	if len(addrs) > 5 {
		n.Text += fmt.Sprintf(" and %d more", len(addrs)-5)
	}
	if s.URL != nil {
		n.URL = s.URL() + "/graph?root=" + t.root + "&env=" + t.env
	}
	if err := s.Notify(ctx, cfg.Drift, n); err != nil {
		s.Log.Warn("drift notification failed", "err", err)
		return
	}
	_ = s.Store.SetKV(key, cur)
}

// Send delivers a notice by the configured channel.
func Send(ctx context.Context, d config.Drift, n Notice) error {
	switch d.Notify {
	case "webhook":
		return webhook(ctx, d.Webhook, n)
	case "none":
		return nil
	}
	return desktop(ctx, "groundwork: drift in "+n.Env, n.Text)
}

func webhook(ctx context.Context, url string, n Notice) error {
	b, _ := json.Marshal(n)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "groundwork")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("webhook answered %s", res.Status)
	}
	return nil
}

// desktop shows a notification; text goes in as argv, never into a script.
func desktop(ctx context.Context, title, text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "osascript",
			"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, text)
	default:
		cmd = exec.CommandContext(ctx, "notify-send", "--app-name=groundwork", title, text)
	}
	return cmd.Run()
}
