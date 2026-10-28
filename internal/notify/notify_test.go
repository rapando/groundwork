package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rapando/groundwork/v2/internal/config"
	"github.com/rapando/groundwork/v2/internal/events"
	"github.com/rapando/groundwork/v2/internal/runner"
	"github.com/rapando/groundwork/v2/internal/store"
	"github.com/rapando/groundwork/v2/internal/terraform"
)

type sent struct {
	mu       sync.Mutex
	desktop  []string // title | text
	webhooks []Payload
}

type rigT struct {
	n    *Notifier
	st   *store.Store
	cfg  *config.Config
	out  *sent
	diag []int64
}

func rig(t *testing.T) *rigT {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &rigT{st: st, cfg: &config.Config{Version: 1, Mode: "standalone"}, out: &sent{}}
	n := New(st, events.NewBus(), func() *config.Config { return r.cfg }, nil)
	n.Project = "infra"
	n.Desktop = func(_ context.Context, title, text string) error {
		r.out.mu.Lock()
		defer r.out.mu.Unlock()
		r.out.desktop = append(r.out.desktop, title+" | "+text)
		return nil
	}
	n.Webhook = func(_ context.Context, url string, p any) error {
		r.out.mu.Lock()
		defer r.out.mu.Unlock()
		r.out.webhooks = append(r.out.webhooks, p.(Payload))
		return nil
	}
	n.Diagnose = func(id int64) { r.diag = append(r.diag, id) }
	r.n = n
	return r
}

// run creates a run that took dur and ended in status.
func (r *rigT) run(t *testing.T, kind string, tg runner.Target, status, stage string, dur time.Duration, sum runner.Summary) int64 {
	t.Helper()
	run, err := r.st.CreateRun(kind, tg, []string{}, "")
	if err != nil {
		t.Fatal(err)
	}
	end := time.Now()
	start := end.Add(-dur)
	u := store.RunUpdate{Status: status, Stage: &stage, Started: &start, Summary: sum}
	if status != store.StatusWaitingApproval {
		u.Ended = &end
	}
	if err := r.st.UpdateRun(run.ID, u); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

var prod = runner.Target{Root: "terraform/envs/prod", Env: "prod", Binary: "terraform"}

func TestFailedRunWithCause(t *testing.T) {
	r := rig(t)
	id := r.run(t, runner.KindPlan, prod, store.StatusFailed, "apply", 161*time.Second, runner.Summary{Error: "apply: exit status 1: Error acquiring the state lock\nmore"})
	r.n.Cause = func(run int64) *store.Cause {
		if !slices.Contains(r.diag, run) {
			t.Error("cause looked up before the issue engine ran")
		}
		return &store.Cause{IssueID: 4, RuleID: "tf-state-lock", Title: "The state is locked", Action: "Unlock the state"}
	}
	no := r.n.Handle(context.Background(), id, store.StatusFailed)
	r.n.Wait()
	if no == nil || no.Event != "failed" || no.Title != "Apply #1 failed on prod" || no.Label != "terraform apply" ||
		no.Text != "terraform/envs/prod · 2m 41s · The state is locked" || no.Cause == nil || no.Cause.IssueID != 4 ||
		!no.Toast || !no.Desktop || no.DurationS != 161 || no.Stage != "apply" || no.Kind != "tf.plan" {
		t.Fatalf("%+v", no)
	}
	if len(r.out.desktop) != 1 || r.out.desktop[0] != "groundwork: Apply #1 failed on prod | terraform/envs/prod · 2m 41s · The state is locked" {
		t.Fatalf("desktop %v", r.out.desktop)
	}
	if len(r.out.webhooks) != 0 {
		t.Fatal("no webhook is configured")
	}
	// once per (run, event)
	if again := r.n.Handle(context.Background(), id, store.StatusFailed); again != nil {
		t.Fatal("notified twice")
	}
	if l, _ := r.st.ListNotifications(10); len(l) != 1 || r.st.Unread() != 1 {
		t.Fatalf("%+v", l)
	}
	// without a cause: the first error line
	r.n.Cause = func(int64) *store.Cause { return nil }
	id = r.run(t, runner.KindPlan, prod, store.StatusFailed, "plan", 3*time.Second, runner.Summary{Error: "plan: exit status 1: ╷\n│ Error: Invalid reference\n"})
	if no := r.n.Handle(context.Background(), id, store.StatusFailed); no == nil || no.Text != "terraform/envs/prod · 3s · Invalid reference" || no.Title != "Plan #2 failed on prod" {
		t.Fatalf("%+v", no)
	}
}

func TestApprovalCounts(t *testing.T) {
	r := rig(t)
	sum := runner.Summary{Plan: &terraform.PlanSummary{Create: 3, Update: 1, Delete: 2}, Danger: []terraform.Danger{{Address: "aws_db_instance.main"}}}
	id := r.run(t, runner.KindPlan, prod, store.StatusWaitingApproval, "approve", 40*time.Second, sum)
	no := r.n.Handle(context.Background(), id, store.StatusWaitingApproval)
	r.n.Wait()
	if no == nil || no.Event != "approval" || no.Title != "Plan #1 is ready for review" || no.Plan == nil || *no.Plan != (store.PlanCounts{Create: 3, Update: 1, Delete: 2, Danger: 1}) ||
		no.Text != "terraform/envs/prod (prod) · 3 to add, 1 to change, 0 to replace, 2 to destroy · 1 dangerous change" || !no.Toast || !no.Desktop {
		t.Fatalf("%+v", no)
	}
	// an approval event that arrives after the plan was approved is dropped
	id = r.run(t, runner.KindPlan, prod, store.StatusRunning, "apply", time.Second, sum)
	if no := r.n.Handle(context.Background(), id, store.StatusWaitingApproval); no != nil {
		t.Fatalf("stale approval: %+v", no)
	}
	// ansible dry run
	ans := runner.Target{Tool: "ansible", Project: "ansible", Env: "dev", Playbook: "playbooks/site.yml"}
	id = r.run(t, runner.KindAnsPlaybook, ans, store.StatusWaitingApproval, "approve", time.Second, runner.Summary{})
	if no := r.n.Handle(context.Background(), id, store.StatusWaitingApproval); no == nil || no.Title != "Dry run of site.yml is ready for review" || no.Root != "ansible" || no.Label != "ansible-playbook site.yml" {
		t.Fatalf("%+v", no)
	}
}

func TestSuccessQuietUnderAndCancelled(t *testing.T) {
	r := rig(t)
	ans := runner.Target{Tool: "ansible", Project: "ansible", Env: "dev", Playbook: "site.yml"}
	short := r.run(t, runner.KindAnsPlaybook, ans, store.StatusSucceeded, "run", 5*time.Second, runner.Summary{})
	no := r.n.Handle(context.Background(), short, store.StatusSucceeded)
	if no == nil || no.Title != "Playbook site.yml succeeded on dev" || no.Toast || no.Desktop {
		t.Fatalf("short success should be quiet: %+v", no)
	}
	long := r.run(t, runner.KindPlan, prod, store.StatusSucceeded, "apply", time.Minute, runner.Summary{Apply: &runner.ApplySummary{Added: 1}})
	no = r.n.Handle(context.Background(), long, store.StatusSucceeded)
	if no == nil || !no.Toast || no.Desktop || no.Text != "terraform/envs/prod · 1m 0s · 1 added, 0 changed, 0 destroyed" {
		t.Fatalf("%+v", no)
	}
	c := r.run(t, runner.KindPlan, prod, store.StatusCancelled, "plan", time.Minute, runner.Summary{})
	if no := r.n.Handle(context.Background(), c, store.StatusCancelled); no == nil || no.Toast || no.Desktop || no.Title != "Plan #3 was cancelled on prod" {
		t.Fatalf("%+v", no)
	}
	r.n.Wait()
	if len(r.out.desktop) != 0 {
		t.Fatalf("%v", r.out.desktop)
	}
}

func TestDriftRunIsRecordedNotSent(t *testing.T) {
	r := rig(t)
	r.cfg.Notify = &config.Notify{Drift: []string{"console", "desktop", "webhook"}, Webhook: "https://hooks.example.com/x"}
	id := r.run(t, runner.KindDrift, prod, store.StatusSucceeded, "plan", time.Minute, runner.Summary{Drift: &runner.DriftSummary{Resources: 2, Attrs: 5}})
	no := r.n.Handle(context.Background(), id, store.StatusSucceeded)
	r.n.Wait()
	if no == nil || no.Event != "drift" || no.Title != "Drift on prod: 2 resources" || no.DriftCount != 2 || !no.Toast || !no.Desktop {
		t.Fatalf("%+v", no)
	}
	if len(r.out.desktop)+len(r.out.webhooks) != 0 {
		t.Fatal("drift is delivered by the drift schedule, once per change")
	}
	// no drift: a plain success
	id = r.run(t, runner.KindDrift, prod, store.StatusSucceeded, "plan", time.Minute, runner.Summary{Drift: &runner.DriftSummary{}})
	if no := r.n.Handle(context.Background(), id, store.StatusSucceeded); no == nil || no.Event != "succeeded" || no.Title != "Drift check #2 succeeded on prod" {
		t.Fatalf("%+v", no)
	}
}

func TestWebhookEnvsAndOpenConsole(t *testing.T) {
	r := rig(t)
	r.cfg.Notify = &config.Notify{Envs: []string{"prod"}, Webhook: "https://hooks.example.com/x"}
	dev := runner.Target{Root: "terraform/envs/dev", Env: "dev"}
	id := r.run(t, runner.KindPlan, dev, store.StatusFailed, "plan", time.Second, runner.Summary{Error: "boom"})
	if no := r.n.Handle(context.Background(), id, store.StatusFailed); no == nil || !no.Toast || no.Desktop {
		t.Fatalf("dev is outside envs: %+v", no)
	}
	r.n.Wait()
	if len(r.out.desktop)+len(r.out.webhooks) != 0 {
		t.Fatal("envs filter")
	}
	open := 1
	r.n.Streams = func() int { return open }
	id = r.run(t, runner.KindPlan, prod, store.StatusFailed, "apply", time.Second, runner.Summary{Error: "boom"})
	no := r.n.Handle(context.Background(), id, store.StatusFailed)
	r.n.Wait()
	if no == nil || !no.Desktop {
		t.Fatalf("%+v", no)
	}
	if len(r.out.desktop) != 0 {
		t.Fatal("an open console shows the desktop notification itself")
	}
	if len(r.out.webhooks) != 1 {
		t.Fatalf("webhook %v", r.out.webhooks)
	}
	p := r.out.webhooks[0]
	if p.Event != "failed" || p.Project != "infra" || p.Env != "prod" || p.RunID != id || p.Kind != "tf.plan" || p.DurationS != 1 ||
		p.Text != "infra: Apply #2 failed on prod (terraform/envs/prod · 1s · boom)" || p.Root != "terraform/envs/prod" {
		t.Fatalf("%+v", p)
	}
}

func TestRunModes(t *testing.T) {
	r := rig(t)
	if r.n.Mode(1) != ModeDefault {
		t.Fatal("default mode")
	}
	if err := r.n.SetMode(1, "loud"); !errors.Is(err, ErrBadMode) {
		t.Fatal(err)
	}
	cases := []struct {
		mode, status          string
		dur                   time.Duration
		toast, desktop, found bool
	}{
		{ModeNone, store.StatusFailed, time.Minute, false, false, true},
		{ModeFail, store.StatusSucceeded, time.Hour, false, false, true},
		{ModeFail, store.StatusFailed, time.Second, true, true, true},
		{ModeFinish, store.StatusSucceeded, time.Second, true, true, true}, // quiet_under doesn't apply
		{ModeDefault, store.StatusSucceeded, time.Hour, true, false, true},
	}
	for _, c := range cases {
		id := r.run(t, runner.KindPlan, prod, c.status, "apply", c.dur, runner.Summary{})
		if err := r.n.SetMode(id, c.mode); err != nil {
			t.Fatal(err)
		}
		if r.n.Mode(id) != c.mode {
			t.Fatal("mode not stored")
		}
		no := r.n.Handle(context.Background(), id, c.status)
		if no == nil || no.Toast != c.toast || no.Desktop != c.desktop {
			t.Errorf("%s/%s: %+v", c.mode, c.status, no)
		}
	}
}

func TestDecide(t *testing.T) {
	s := (&config.Config{Notify: &config.Notify{Success: []string{"console", "desktop", "webhook"}, Envs: []string{"prod"}, QuietUnder: "0s", Webhook: "https://h/x"}}).NotifySettings()
	if to, de, wh := decide(s, "succeeded", "prod", 0, ModeDefault); !to || !de || !wh {
		t.Fatal("all channels")
	}
	if to, de, wh := decide(s, "succeeded", "dev", 0, ModeDefault); !to || de || wh {
		t.Fatal("envs limits desktop and webhook only")
	}
	if to, de, wh := decide(s, "succeeded", "dev", 0, ModeFinish); !to || !de || wh {
		t.Fatal("finish: desktop whatever the env, webhook still filtered")
	}
}

func TestStartFollowsTheBus(t *testing.T) {
	r := rig(t)
	ctx, cancel := context.WithCancel(context.Background())
	sub, unsub := r.n.Bus.Subscribe()
	defer unsub()
	done := r.n.Start(ctx)
	id := r.run(t, runner.KindPlan, prod, store.StatusFailed, "plan", time.Second, runner.Summary{})
	// the loop subscribes asynchronously: keep publishing until it answers
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(5 * time.Second)
	var got store.Notification
	var unread map[string]int
wait:
	for {
		select {
		case <-tick.C:
			r.n.Bus.Publish("run.updated", map[string]any{"id": id, "status": "failed"})
		case ev := <-sub:
			switch ev.Type {
			case EventNew:
				_ = json.Unmarshal(ev.Data, &got)
			case EventUpdated:
				_ = json.Unmarshal(ev.Data, &unread)
				break wait
			}
		case <-deadline:
			t.Fatal("no notification")
		}
	}
	if got.RunID != id || unread["unread"] != 1 {
		t.Fatalf("%+v %v", got, unread)
	}
	cancel()
	<-done
}

func TestSweepAndPrune(t *testing.T) {
	r := rig(t)
	r.n.started = time.Now().Add(-time.Minute)
	id := r.run(t, runner.KindPlan, prod, store.StatusFailed, "plan", time.Second, runner.Summary{})
	r.n.Sweep(context.Background())
	if !r.st.HasNotification(id, "failed") {
		t.Fatal("sweep missed a run")
	}
	r.n.SetMode(9999, ModeNone) // a run that is gone
	r.n.Now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }
	r.n.Prune()
	if l, _ := r.st.ListNotifications(10); len(l) != 0 {
		t.Fatal("not pruned")
	}
	if _, ok, _ := r.st.GetKV(modeKey(9999)); ok {
		t.Fatal("mode of a removed run kept")
	}
}

func TestTestSend(t *testing.T) {
	r := rig(t)
	sentTo, errs := r.n.Test(context.Background())
	if !slices.Equal(sentTo, []string{"desktop"}) || len(errs) != 0 {
		t.Fatalf("%v %v", sentTo, errs)
	}
	r.cfg.Drift.Webhook = "https://hooks.example.com/x" // failed gains the webhook channel by default
	r.n.Desktop = func(context.Context, string, string) error { return errors.New("no notify-send") }
	r.n.Webhook = func(context.Context, string, any) error { return nil }
	sentTo, errs = r.n.Test(context.Background())
	if !slices.Equal(sentTo, []string{"webhook"}) || len(errs) != 1 || errs[0].Channel != "desktop" {
		t.Fatalf("%v %v", sentTo, errs)
	}
	// nothing in use: nothing sent
	r.cfg.Notify = &config.Notify{Failed: []string{}, Approval: []string{"console"}, Success: []string{}, Drift: []string{"console"}}
	if sentTo, errs = r.n.Test(context.Background()); len(sentTo)+len(errs) != 0 {
		t.Fatalf("%v %v", sentTo, errs)
	}
	r.cfg.Notify = nil
	if err := r.n.External(context.Background(), []string{"desktop", "webhook"}, "t", "x", nil); err == nil || !strings.Contains(err.Error(), "desktop") {
		t.Fatal(err)
	}
}

func TestPostWebhook(t *testing.T) {
	var got Payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(400)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()
	if err := PostWebhook(context.Background(), srv.URL, Payload{Text: "x", Event: "failed"}); err != nil || got.Event != "failed" {
		t.Fatalf("%v %+v", err, got)
	}
	redirect := httptest.NewServer(http.RedirectHandler("http://example.invalid/", http.StatusFound))
	defer redirect.Close()
	if err := PostWebhook(context.Background(), redirect.URL, Payload{}); err == nil {
		t.Fatal("a redirect is not followed, and is an error")
	}
	if err := PostWebhook(context.Background(), "http://127.0.0.1:1/", Payload{}); err == nil {
		t.Fatal("connection refused")
	}
}

// Desktop text reaches a subprocess: it must stay argv, never script or options.
func TestDesktopCommandKeepsTextAsArguments(t *testing.T) {
	hostile := []string{"-e", `display dialog "pwned"`, `"; do shell script "id" --`, "--app-name=evil", "$(id)"}
	for _, goos := range []string{"darwin", "linux"} {
		fixed := desktopCmd(context.Background(), goos, "T", "X").Args
		fixed = fixed[:len(fixed)-2]
		if fixed[len(fixed)-1] != "--" {
			t.Fatalf("%s: options are not ended: %q", goos, fixed)
		}
		for _, h := range hostile {
			args := desktopCmd(context.Background(), goos, h, h).Args
			if !slices.Equal(args[:len(args)-2], fixed) || args[len(args)-2] != h || args[len(args)-1] != h {
				t.Fatalf("%s: %q", goos, args)
			}
		}
	}
}

func TestFormatting(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "0s", 45 * time.Second: "45s", 161 * time.Second: "2m 41s", 63 * time.Minute: "1h 3m"} {
		if got := FormatDuration(d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
	if l := errorLine(strings.Repeat("x", 200)); len([]rune(l)) != 120 {
		t.Fatal("long lines are cut")
	}
	if errorLine("\n\n") != "" {
		t.Fatal("empty")
	}
	labels := map[string]string{
		runner.KindDrift: "terraform plan -refresh-only", runner.KindInit: "terraform init", runner.KindUnlock: "terraform force-unlock",
		runner.KindAnsCheck: "ansible-playbook --check playbook", runner.KindAnsAdhoc: "ansible all -m ping",
		runner.KindAnsPing: "ansible -m ping", runner.KindAnsFacts: "ansible -m setup", "x.y": "x.y",
	}
	for k, want := range labels {
		if got := Label(k, "", runner.Target{Pattern: "all", Module: "ping"}); got != want {
			t.Errorf("%s: %q", k, got)
		}
	}
	destroy := runner.Target{Destroy: true}
	if Label(runner.KindPlan, "plan", destroy) != "terraform plan -destroy" || Label(runner.KindPlan, "apply", destroy) != "terraform destroy" {
		t.Error("a destroy plan is labelled as one")
	}
	if subject(&store.Run{ID: 4, Kind: runner.KindPlan, Stage: "apply"}, destroy) != "Destroy #4" || subject(&store.Run{ID: 4, Kind: runner.KindPlan}, destroy) != "Destroy plan #4" {
		t.Error("a destroy's notification says so")
	}
	for _, k := range []string{runner.KindDrift, runner.KindInit, runner.KindUnlock, runner.KindAnsCheck, runner.KindAnsAdhoc, runner.KindAnsPing, runner.KindAnsFacts, "x"} {
		if s := subject(&store.Run{ID: 3, Kind: k}, runner.Target{}); !strings.Contains(s, "3") && !strings.Contains(s, "playbook") {
			t.Errorf("%s: %q", k, s)
		}
	}
}
