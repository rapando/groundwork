// Package notify turns run events into notifications: a list kept in the
// project's store (shown in the console with toasts), desktop notifications
// and a webhook.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/runner"
	"github.com/rapando/groundwork/internal/safego"
	"github.com/rapando/groundwork/internal/store"
)

// Per-run override modes.
const (
	ModeDefault = "default" // the project's settings
	ModeFinish  = "finish"  // tell me whatever happens
	ModeFail    = "fail"    // only if it fails
	ModeNone    = "none"    // record only
)

// Modes lists the valid per-run modes.
var Modes = []string{ModeDefault, ModeFinish, ModeFail, ModeNone}

// Bus events.
const (
	EventNew     = "notification.new"      // data: store.Notification
	EventUpdated = "notifications.updated" // data: {unread}
)

// Payload is the webhook body: a Slack/Teams-compatible text plus fields.
type Payload struct {
	Text      string       `json:"text"`
	Event     string       `json:"event"`
	Title     string       `json:"title"`
	Project   string       `json:"project"`
	Root      string       `json:"root"`
	Env       string       `json:"env"`
	RunID     int64        `json:"run_id"`
	Kind      string       `json:"kind"`
	DurationS int64        `json:"duration_s"`
	Cause     *store.Cause `json:"cause,omitempty"`
}

// Notifier watches a project's bus and records and delivers notifications.
// Fields left nil fall back to the real implementations (or are skipped).
type Notifier struct {
	Store *store.Store
	Bus   *events.Bus
	Cfg   func() *config.Config
	// Base returns the settings for every project, under the project's own
	// notify: block. Optional.
	Base func() *config.Notify
	Log  *slog.Logger
	// OnPanic is told when the loop or a delivery panics.
	OnPanic safego.Hook

	// Project names the project in webhook payloads.
	Project string
	// Diagnose runs the issue engine on a failed run and returns once the
	// run's issues are stored, so the cause can be linked. Optional.
	Diagnose func(runID int64)
	// Cause finds the Troubleshoot issue for a run (after Diagnose). Optional.
	Cause func(runID int64) *store.Cause
	// Streams counts the console event streams open right now. The service
	// shows desktop notifications itself only when there are none: an open
	// console shows them through the browser. nil counts as none.
	Streams func() int
	// Desktop and Webhook deliver; tests replace them.
	Desktop func(ctx context.Context, title, text string) error
	Webhook func(ctx context.Context, url string, payload any) error
	// Now is the clock (tests).
	Now func() time.Time

	started time.Time
	mu      sync.Mutex // serialises Handle so dedupe and unread counts stay consistent
	wg      sync.WaitGroup
}

// New returns a notifier with the real senders.
func New(st *store.Store, bus *events.Bus, cfg func() *config.Config, log *slog.Logger) *Notifier {
	if log == nil {
		log = slog.Default()
	}
	return &Notifier{Store: st, Bus: bus, Cfg: cfg, Log: log, Desktop: Desktop, Webhook: PostWebhook}
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *Notifier) settings() (config.NotifySettings, *config.Config) {
	var c *config.Config
	if n.Cfg != nil {
		c = n.Cfg()
	}
	var base *config.Notify
	if n.Base != nil {
		base = n.Base()
	}
	return c.NotifySettingsOver(base), c
}

// Start follows run events until ctx ends. The returned channel is closed
// once the loop and every delivery it started have finished.
func (n *Notifier) Start(ctx context.Context) <-chan struct{} {
	n.started = n.now()
	loop := safego.Loop(ctx, n.Log, "notifier", n.OnPanic, func(ctx context.Context) {
		ch, cancel := n.Bus.Subscribe()
		defer cancel()
		n.Prune()
		sweep := time.NewTicker(30 * time.Second)
		defer sweep.Stop()
		prune := time.NewTicker(time.Hour)
		defer prune.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if ev.Type != "run.updated" {
					continue
				}
				var d struct {
					ID     int64  `json:"id"`
					Status string `json:"status"`
				}
				if json.Unmarshal(ev.Data, &d) == nil && notifies(d.Status) {
					n.Handle(ctx, d.ID, d.Status)
				}
			case <-sweep.C:
				n.Sweep(ctx)
			case <-prune.C:
				n.Prune()
			}
		}
	})
	done := make(chan struct{})
	go func() {
		<-loop
		n.wg.Wait()
		close(done)
	}()
	return done
}

// Wait blocks until deliveries in flight have finished (tests).
func (n *Notifier) Wait() { n.wg.Wait() }

func notifies(status string) bool {
	switch status {
	case store.StatusFailed, store.StatusSucceeded, store.StatusCancelled, store.StatusWaitingApproval:
		return true
	}
	return false
}

// Sweep catches up on runs that ended since Start without an event reaching
// the notifier (the bus drops events for a slow subscriber).
func (n *Notifier) Sweep(ctx context.Context) {
	runs, err := n.Store.ListRuns(store.RunFilter{Limit: 20})
	if err != nil {
		return
	}
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		at := r.CreatedAt
		if r.EndedAt != nil {
			at = *r.EndedAt
		}
		if notifies(r.Status) && !at.Before(n.started) {
			n.Handle(ctx, r.ID, r.Status)
		}
	}
}

// Prune drops notifications older than 30 days, and the per-run modes of
// runs that are gone.
func (n *Notifier) Prune() {
	_, _ = n.Store.PruneRunKV(modePrefix)
	if c, err := n.Store.PruneNotifications(n.now().Add(-store.NotificationMaxAge)); err == nil && c > 0 {
		n.publishUnread()
	}
}

func (n *Notifier) publishUnread() {
	n.Bus.Publish(EventUpdated, map[string]int{"unread": n.Store.Unread()})
}

// ---- per-run override ----

const modePrefix = "notify.run."

func modeKey(runID int64) string { return fmt.Sprintf("%s%d", modePrefix, runID) }

// Mode returns the run's override (default when none was set).
func (n *Notifier) Mode(runID int64) string {
	if v, ok, _ := n.Store.GetKV(modeKey(runID)); ok && slices.Contains(Modes, v) {
		return v
	}
	return ModeDefault
}

// ErrBadMode is returned for a mode that isn't one of Modes.
var ErrBadMode = errors.New("mode must be default, finish, fail or none")

// SetMode stores the run's override.
func (n *Notifier) SetMode(runID int64, mode string) error {
	if !slices.Contains(Modes, mode) {
		return ErrBadMode
	}
	return n.Store.SetKV(modeKey(runID), mode)
}

// ---- building ----

// event maps a run status (and what the run found) to a notification event.
func event(status string, run *store.Run, sum runner.Summary) string {
	switch status {
	case store.StatusFailed:
		return store.EventFailed
	case store.StatusCancelled:
		return store.EventCancelled
	case store.StatusWaitingApproval:
		return store.EventApproval
	case store.StatusSucceeded:
		if run.Kind == runner.KindDrift && sum.Drift != nil && sum.Drift.Resources > 0 {
			return store.EventDrift
		}
		return store.EventSucceeded
	}
	return ""
}

// Handle records the notification for a run reaching status (once per run
// and event) and delivers it. It returns the notification, or nil when there
// was nothing new to record.
func (n *Notifier) Handle(ctx context.Context, runID int64, status string) *store.Notification {
	n.mu.Lock()
	defer n.mu.Unlock()
	run, err := n.Store.GetRun(runID)
	if err != nil {
		return nil
	}
	// a stale event: the run has moved on (a plan approved or discarded
	// before we got here needs no "ready for review")
	if status == store.StatusWaitingApproval && run.Status != status {
		return nil
	}
	var sum runner.Summary
	_ = json.Unmarshal(run.Summary, &sum)
	ev := event(status, run, sum)
	if ev == "" || n.Store.HasNotification(runID, ev) {
		return nil
	}
	if ev == store.EventFailed && n.Diagnose != nil {
		n.Diagnose(runID) // the issue engine works on the same event; make sure it has
	}
	var cause *store.Cause
	if ev == store.EventFailed && n.Cause != nil {
		cause = n.Cause(runID)
	}
	no := n.build(run, ev, sum, cause)
	s, _ := n.settings()
	mode := n.Mode(runID)
	toast, desktop, webhook := decide(s, ev, no.Env, time.Duration(no.DurationS)*time.Second, mode)
	no.Toast, no.Desktop = toast, desktop
	if ok, err := n.Store.AddNotification(no); err != nil || !ok {
		if err != nil {
			n.Log.Warn("record notification", "run", runID, "err", err)
		}
		return nil
	}
	n.Bus.Publish(EventNew, no)
	n.publishUnread()
	// drift reaches the desktop and the webhook through the drift schedule,
	// once per change; a manual check is seen in the console
	if ev == store.EventDrift {
		return no
	}
	var chans []string
	if desktop {
		chans = append(chans, config.ChannelDesktop)
	}
	if webhook && s.Webhook != "" {
		chans = append(chans, config.ChannelWebhook)
	}
	if len(chans) > 0 {
		p := n.payload(no)
		n.wg.Add(1)
		safego.Go(n.Log, "notification delivery", n.OnPanic, func() {
			defer n.wg.Done()
			if err := n.External(ctx, chans, no.Title, no.Text, p); err != nil {
				n.Log.Warn("notification delivery failed", "run", runID, "event", ev, "err", err)
			}
		})
	}
	return no
}

// decide applies settings and the run's mode: whether the console toasts,
// whether the notification should reach the desktop, and whether the webhook
// is sent.
func decide(s config.NotifySettings, ev, env string, dur time.Duration, mode string) (toast, desktop, webhook bool) {
	if ev == store.EventCancelled || mode == ModeNone {
		return false, false, false
	}
	chans := s.Channels(ev)
	has := func(c string) bool { return slices.Contains(chans, c) }
	envOK := s.EnvAllowed(env)
	switch mode {
	case ModeFail:
		if ev != store.EventFailed {
			return false, false, false
		}
	case ModeFinish: // asked for this run: console and desktop whatever the settings say
		return true, true, has(config.ChannelWebhook) && envOK
	}
	if ev == store.EventSucceeded && dur < s.Quiet() {
		return false, false, false
	}
	return has(config.ChannelConsole), has(config.ChannelDesktop) && envOK, has(config.ChannelWebhook) && envOK
}

// External delivers by desktop and/or webhook. The desktop notification is
// shown by the service only when no console is open (the console shows it
// through the browser, where a click opens the run).
func (n *Notifier) External(ctx context.Context, chans []string, title, text string, payload any) error {
	s, _ := n.settings()
	var errs []error
	for _, ch := range chans {
		switch ch {
		case config.ChannelDesktop:
			if n.Streams != nil && n.Streams() > 0 {
				continue
			}
			if n.Desktop != nil {
				if err := n.Desktop(ctx, "groundwork: "+title, text); err != nil {
					errs = append(errs, fmt.Errorf("desktop: %w", err))
				}
			}
		case config.ChannelWebhook:
			if s.Webhook != "" && n.Webhook != nil {
				if err := n.Webhook(ctx, s.Webhook, payload); err != nil {
					errs = append(errs, fmt.Errorf("webhook: %w", err))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// TestError is one channel a test send failed on.
type TestError struct {
	Channel string `json:"channel"`
	Error   string `json:"error"`
}

// Test sends a test notification to the channels the settings use: the
// desktop (shown by the service, whether or not a console is open) if any
// event lists it, and the webhook if any event lists it and a URL is set.
func (n *Notifier) Test(ctx context.Context) (sent []string, errs []TestError) {
	s, _ := n.settings()
	uses := func(ch string) bool {
		for _, l := range [][]string{s.Failed, s.Approval, s.Success, s.Drift} {
			if slices.Contains(l, ch) {
				return true
			}
		}
		return false
	}
	title, text := "groundwork: test notification", "Notifications from "+n.projectName()+" reach you here."
	sent, errs = []string{}, []TestError{}
	if uses(config.ChannelDesktop) && n.Desktop != nil {
		if err := n.Desktop(ctx, title, text); err != nil {
			errs = append(errs, TestError{config.ChannelDesktop, err.Error()})
		} else {
			sent = append(sent, config.ChannelDesktop)
		}
	}
	if uses(config.ChannelWebhook) && s.Webhook != "" && n.Webhook != nil {
		p := Payload{Text: n.projectName() + ": test notification from groundwork", Event: "test", Title: "Test notification", Project: n.projectName()}
		if err := n.Webhook(ctx, s.Webhook, p); err != nil {
			errs = append(errs, TestError{config.ChannelWebhook, err.Error()})
		} else {
			sent = append(sent, config.ChannelWebhook)
		}
	}
	return sent, errs
}

func (n *Notifier) projectName() string {
	if n.Project != "" {
		return n.Project
	}
	return "groundwork"
}

func (n *Notifier) payload(no *store.Notification) Payload {
	text := n.projectName() + ": " + no.Title
	if no.Text != "" {
		text += " (" + no.Text + ")"
	}
	return Payload{Text: text, Event: no.Event, Title: no.Title, Project: n.projectName(), Root: no.Root, Env: no.Env,
		RunID: no.RunID, Kind: no.Kind, DurationS: no.DurationS, Cause: no.Cause}
}

func (n *Notifier) build(run *store.Run, ev string, sum runner.Summary, cause *store.Cause) *store.Notification {
	var t runner.Target
	_ = json.Unmarshal(run.Target, &t)
	root := t.Root
	if root == "" {
		root = t.Project
	}
	no := &store.Notification{Event: ev, RunID: run.ID, Kind: run.Kind, Stage: run.Stage, Label: Label(run.Kind, run.Stage, t),
		Root: root, Env: t.Env, Cause: cause, At: n.now()}
	if run.StartedAt != nil {
		end := n.now()
		if run.EndedAt != nil {
			end = *run.EndedAt
		}
		if d := end.Sub(*run.StartedAt); d > 0 {
			no.DurationS = int64(d.Round(time.Second) / time.Second)
		}
	}
	dur := FormatDuration(time.Duration(no.DurationS) * time.Second)
	what := subject(run, t)
	on := ""
	if t.Env != "" {
		on = " on " + t.Env
	}
	parts := []string{root}
	switch ev {
	case store.EventFailed:
		no.Title = what + " failed" + on
		parts = append(parts, dur)
		if cause != nil && cause.Title != "" {
			parts = append(parts, cause.Title)
		} else if l := errorLine(sum.Error); l != "" {
			parts = append(parts, l)
		}
	case store.EventCancelled:
		no.Title = what + " was cancelled" + on
		parts = append(parts, dur)
	case store.EventSucceeded:
		no.Title = what + " succeeded" + on
		parts = append(parts, dur)
		switch {
		case sum.NoChanges:
			parts = append(parts, "no changes")
		case sum.Apply != nil:
			parts = append(parts, fmt.Sprintf("%d added, %d changed, %d destroyed", sum.Apply.Added, sum.Apply.Changed, sum.Apply.Destroyed))
		}
	case store.EventApproval:
		if strings.HasPrefix(run.Kind, "ans.") {
			no.Title = fmt.Sprintf("Dry run of %s is ready for review", playbookName(t))
		} else {
			no.Title = fmt.Sprintf("Plan #%d is ready for review", run.ID)
		}
		if p := sum.Plan; p != nil {
			no.Plan = &store.PlanCounts{Create: p.Create, Update: p.Update, Replace: p.Replace, Delete: p.Delete, Danger: len(sum.Danger)}
			parts = append(parts, fmt.Sprintf("%d to add, %d to change, %d to replace, %d to destroy", p.Create, p.Update, p.Replace, p.Delete))
			if d := len(sum.Danger); d > 0 {
				parts = append(parts, plural(d, "dangerous change"))
			}
		}
		if t.Env != "" {
			parts[0] = root + " (" + t.Env + ")"
		}
	case store.EventDrift:
		c := 0
		if sum.Drift != nil {
			c = sum.Drift.Resources
		}
		no.DriftCount = c
		env := t.Env
		if env == "" {
			env = root
		}
		no.Title = fmt.Sprintf("Drift on %s: %s", env, plural(c, "resource"))
		parts = append(parts, plural(c, "resource")+" changed outside Terraform")
	}
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	no.Text = strings.Join(keep, " · ")
	return no
}

// subject names the run in a title: "Apply #14", "Plan #15", "Playbook site.yml".
func subject(run *store.Run, t runner.Target) string {
	switch run.Kind {
	case runner.KindPlan:
		switch {
		case t.Destroy && run.Stage == "apply":
			return fmt.Sprintf("Destroy #%d", run.ID)
		case t.Destroy:
			return fmt.Sprintf("Destroy plan #%d", run.ID)
		case run.Stage == "apply":
			return fmt.Sprintf("Apply #%d", run.ID)
		}
		return fmt.Sprintf("Plan #%d", run.ID)
	case runner.KindDrift:
		return fmt.Sprintf("Drift check #%d", run.ID)
	case runner.KindInit:
		return fmt.Sprintf("Init #%d", run.ID)
	case runner.KindUnlock:
		return fmt.Sprintf("Unlock #%d", run.ID)
	case runner.KindAnsPlaybook:
		return "Playbook " + playbookName(t)
	case runner.KindAnsCheck:
		return "Dry run of " + playbookName(t)
	case runner.KindAnsAdhoc:
		return fmt.Sprintf("Command #%d", run.ID)
	case runner.KindAnsPing:
		return fmt.Sprintf("Ping #%d", run.ID)
	case runner.KindAnsFacts:
		return fmt.Sprintf("Fact gathering #%d", run.ID)
	}
	return fmt.Sprintf("Run #%d", run.ID)
}

func playbookName(t runner.Target) string {
	if t.Playbook == "" {
		return "playbook"
	}
	return path.Base(t.Playbook)
}

// Label is the command a run stands for: "terraform apply",
// "ansible-playbook site.yml".
func Label(kind, stage string, t runner.Target) string {
	bin := t.Binary
	if bin == "" {
		bin = "terraform"
	}
	switch kind {
	case runner.KindPlan:
		switch {
		case t.Destroy && stage == "apply":
			return bin + " destroy"
		case t.Destroy:
			return bin + " plan -destroy"
		case stage == "apply":
			return bin + " apply"
		}
		return bin + " plan"
	case runner.KindDrift:
		return bin + " plan -refresh-only"
	case runner.KindInit:
		return bin + " init"
	case runner.KindUnlock:
		return bin + " force-unlock"
	case runner.KindAnsPlaybook:
		return "ansible-playbook " + playbookName(t)
	case runner.KindAnsCheck:
		return "ansible-playbook --check " + playbookName(t)
	case runner.KindAnsAdhoc:
		return strings.TrimSpace("ansible " + t.Pattern + " -m " + t.Module)
	case runner.KindAnsPing:
		return "ansible -m ping"
	case runner.KindAnsFacts:
		return "ansible -m setup"
	}
	return kind
}

var exitPrefixRe = regexp.MustCompile(`^.*?exit status \d+:\s*`)

// errorLine is the first meaningful line of a run's error, without the
// "stage: exit status 1:" prefix and terraform's box drawing.
func errorLine(msg string) string {
	for _, l := range strings.Split(msg, "\n") {
		l = strings.TrimSpace(exitPrefixRe.ReplaceAllString(strings.TrimSpace(l), ""))
		l = strings.TrimSpace(strings.TrimLeft(l, "╷│╵"))
		l = strings.TrimSpace(strings.TrimPrefix(l, "Error:"))
		if l == "" {
			continue
		}
		if r := []rune(l); len(r) > 120 {
			l = string(r[:119]) + "…"
		}
		return l
	}
	return ""
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// FormatDuration renders "2m 41s", "45s", "1h 3m".
func FormatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
