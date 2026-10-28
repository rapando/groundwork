package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rapando/groundwork/v2/internal/config"
	"github.com/rapando/groundwork/v2/internal/drift"
	"github.com/rapando/groundwork/v2/internal/store"
)

func TestNotificationsOverHTTP(t *testing.T) {
	withFakeTerraform(t)
	lock := filepath.Join(t.TempDir(), "locked")
	t.Setenv("FAKE_LOCKED", lock)
	os.WriteFile(lock, nil, 0o644)
	e := newConfigured(t)
	e.a.Issues.PS = func() (string, error) { return "  1 /sbin/init\n", nil }
	var mu sync.Mutex
	var desktop []string
	e.a.Notifier.Desktop = func(_ context.Context, title, _ string) error {
		mu.Lock()
		defer mu.Unlock()
		desktop = append(desktop, title)
		return nil
	}

	rec := call(t, e.h, "POST", "/runs", map[string]any{"kind": "tf.plan", "root": "terraform/envs/dev", "env": "dev"})
	id := into(t, rec)["run"].(map[string]any)["id"].(float64)
	waitRun(t, e, id, "failed")
	// what the notifier does on the run.updated event; the issue engine runs
	// inside it, so the cause is linked without waiting
	n := e.a.Notifier.Handle(context.Background(), int64(id), store.StatusFailed)
	e.a.Notifier.Wait()
	if n == nil || n.Cause == nil || n.Cause.RuleID == "" || n.Cause.Title != "State locked on terraform/envs/dev (dev)" || n.Cause.Action == "" {
		t.Fatalf("%+v", n)
	}
	if !strings.HasSuffix(n.Text, "State locked on terraform/envs/dev (dev)") || n.Title != "Plan #1 failed on dev" {
		t.Fatalf("%+v", n)
	}
	if len(desktop) != 1 {
		t.Fatalf("desktop %v", desktop)
	}

	l := into(t, call(t, e.h, "GET", "/notifications?limit=10", nil))
	items := l["items"].([]any)
	if len(items) != 1 || l["unread"].(float64) != 1 {
		t.Fatalf("%v", l)
	}
	it := items[0].(map[string]any)
	if it["event"] != "failed" || it["toast"] != true || it["cause"].(map[string]any)["issue_id"].(float64) != float64(n.Cause.IssueID) {
		t.Fatalf("%v", it)
	}
	// the run's issues
	is := into(t, call(t, e.h, "GET", "/issues?run="+itoa(int(id)), nil))
	if len(is["open"].([]any)) != 1 {
		t.Fatalf("%v", is)
	}
	// a run's issues keep the log lines they matched (the failure card's raw error)
	if ex, _ := is["open"].([]any)[0].(map[string]any)["excerpt"].(string); !strings.Contains(ex, "state lock") {
		t.Fatalf("excerpt %q", ex)
	}
	if is := into(t, call(t, e.h, "GET", "/issues?run=999", nil)); len(is["open"].([]any)) != 0 || len(is["resolved"].([]any)) != 0 {
		t.Fatalf("%v", is)
	}
	if rec := call(t, e.h, "GET", "/issues?run=x", nil); rec.Code != 400 {
		t.Fatal(rec.Code)
	}

	s := e.a.Summary()
	if s.UnreadNotifications != 1 || s.UnreadFailed != 1 || s.WaitingApproval != 0 {
		t.Fatalf("%+v", s)
	}
	if r := into(t, call(t, e.h, "POST", "/notifications/read", map[string]any{"run_id": id})); r["unread"].(float64) != 0 {
		t.Fatalf("%v", r)
	}
	if r := into(t, call(t, e.h, "POST", "/notifications/read", map[string]any{"all": true})); r["unread"].(float64) != 0 {
		t.Fatalf("%v", r)
	}
	if rec := call(t, e.h, "POST", "/notifications/read", map[string]any{"bogus": 1}); rec.Code != 400 {
		t.Fatal(rec.Code)
	}

	// per-run override
	rid := "/runs/" + itoa(int(id)) + "/notify"
	if m := into(t, call(t, e.h, "GET", rid, nil)); m["mode"] != "default" {
		t.Fatalf("%v", m)
	}
	if m := into(t, call(t, e.h, "PUT", rid, map[string]any{"mode": "finish"})); m["mode"] != "finish" {
		t.Fatalf("%v", m)
	}
	if m := into(t, call(t, e.h, "GET", rid, nil)); m["mode"] != "finish" {
		t.Fatalf("%v", m)
	}
	if rec := call(t, e.h, "PUT", rid, map[string]any{"mode": "loud"}); rec.Code != 400 {
		t.Fatal(rec.Code)
	}
	if rec := call(t, e.h, "GET", "/runs/999/notify", nil); rec.Code != 404 {
		t.Fatal(rec.Code)
	}
	if rec := call(t, e.h, "PUT", "/runs/999/notify", map[string]any{"mode": "none"}); rec.Code != 404 {
		t.Fatal(rec.Code)
	}
	if rec := call(t, e.h, "GET", "/runs/x/notify", nil); rec.Code != 400 {
		t.Fatal(rec.Code)
	}

	// test send: desktop by the service
	tr := into(t, call(t, e.h, "POST", "/notifications/test", nil))
	if s := tr["sent"].([]any); len(s) != 1 || s[0] != "desktop" || len(tr["errors"].([]any)) != 0 {
		t.Fatalf("%v", tr)
	}
}

func TestNotificationSettingsOverHTTP(t *testing.T) {
	e := newConfigured(t)
	p := filepath.Join(e.root, config.FileName)
	orig, _ := os.ReadFile(p)
	withComment := append([]byte("# my team's settings\n"), orig...)
	withComment = append(withComment, []byte("# trailing note\n")...)
	os.WriteFile(p, withComment, 0o644)
	e.a.reloadConfig()

	g := into(t, call(t, e.h, "GET", "/notifications/settings", nil))
	if g["configured"] != false {
		t.Fatalf("%v", g)
	}
	st := g["settings"].(map[string]any)
	if st["quiet_under"] != "30s" || len(st["failed"].([]any)) != 2 || len(st["envs"].([]any)) != 0 {
		t.Fatalf("%v", st)
	}

	body := map[string]any{"failed": []string{"console", "desktop", "webhook"}, "approval": []string{"console"}, "success": []string{},
		"drift": []string{"console", "webhook"}, "quiet_under": "1m", "envs": []string{"prod"}, "webhook": "https://hooks.example.com/T1"}
	rec := call(t, e.h, "PUT", "/notifications/settings", body)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	g = into(t, rec)
	st = g["settings"].(map[string]any)
	if g["configured"] != true || st["webhook"] != "https://hooks.example.com/T1" || len(st["success"].([]any)) != 0 || st["quiet_under"] != "1m" {
		t.Fatalf("%v", g)
	}
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), "# my team's settings\n"+string(orig)) || !strings.Contains(string(b), "# trailing note\nnotify:\n  failed: [console, desktop, webhook]\n") {
		t.Fatalf("file not edited in place:\n%s", b)
	}
	// saving again replaces the block
	body["webhook"] = ""
	body["drift"] = []string{"console"}
	body["failed"] = []string{"console"}
	if rec := call(t, e.h, "PUT", "/notifications/settings", body); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	b, _ = os.ReadFile(p)
	if strings.Count(string(b), "notify:") != 1 || strings.Contains(string(b), "hooks.example.com") {
		t.Fatalf("%s", b)
	}

	for _, bad := range []map[string]any{
		{"failed": []string{"pager"}},
		{"quiet_under": "soon"},
		{"envs": []string{"-chdir=/"}},
		{"webhook": "file:///etc/passwd"},
		{"failed": []string{"webhook"}, "webhook": ""},
		{"extra": true},
	} {
		if rec := call(t, e.h, "PUT", "/notifications/settings", bad); rec.Code != 400 {
			t.Errorf("%v: %d %s", bad, rec.Code, rec.Body)
		}
	}
	if b2, _ := os.ReadFile(p); string(b2) != string(b) {
		t.Fatal("a refused save changed the file")
	}
}

// A project's settings sit over the settings for every project: only what it
// overrides is written to groundwork.yaml, and clearing every override
// removes the block.
func TestNotificationOverridesOverHTTP(t *testing.T) {
	e := newConfigured(t)
	base := &config.Notify{Failed: []string{"console", "webhook"}, QuietUnder: "2m", Webhook: "https://hooks.example.com/all"}
	e.a.MachineNotify = func() *config.Notify { return base }
	p := filepath.Join(e.root, config.FileName)
	orig, _ := os.ReadFile(p)

	g := into(t, call(t, e.h, "GET", "/notifications/settings", nil))
	st, bs, ov := g["settings"].(map[string]any), g["base"].(map[string]any), g["overrides"].(map[string]any)
	if g["shared"] != true || st["webhook"] != base.Webhook || st["quiet_under"] != "2m" || bs["quiet_under"] != "2m" || ov["failed"] != nil || ov["webhook"] != "" {
		t.Fatalf("%v", g)
	}
	if s := e.a.NotifySettings(); s.Webhook != base.Webhook || len(s.Failed) != 2 {
		t.Fatalf("effective %+v", s)
	}
	// the webhook channel is satisfied by the base URL; only the overrides are written
	rec := call(t, e.h, "PUT", "/notifications/settings", map[string]any{"drift": []string{"console", "webhook"}, "envs": []string{}})
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	g = into(t, rec)
	if st := g["settings"].(map[string]any); len(st["drift"].([]any)) != 2 || st["webhook"] != base.Webhook || st["quiet_under"] != "2m" {
		t.Fatalf("%v", g)
	}
	b, _ := os.ReadFile(p)
	if !strings.HasSuffix(string(b), "notify:\n  drift: [console, webhook]\n  envs: []\n") {
		t.Fatalf("%s", b)
	}
	// a change below reaches the project at once
	base = &config.Notify{QuietUnder: "5m"}
	if s := e.a.NotifySettings(); s.QuietUnder != "5m" || len(s.Drift) != 2 {
		t.Fatalf("%+v", s)
	}
	// nothing overridden: the block goes
	if rec := call(t, e.h, "PUT", "/notifications/settings", map[string]any{}); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(p); string(b) != string(orig) {
		t.Fatalf("block not removed:\n%s", b)
	}
	if g := into(t, call(t, e.h, "GET", "/notifications/settings", nil)); g["configured"] != false {
		t.Fatalf("%v", g)
	}
}

func TestNotificationSettingsNeedAConfig(t *testing.T) {
	root := fixture(t, "iac-only")
	h := newAPI(t, root)
	if rec := call(t, h, "PUT", "/notifications/settings", map[string]any{"failed": []string{"console"}}); rec.Code != 409 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if g := into(t, call(t, h, "GET", "/notifications/settings", nil)); g["configured"] != false {
		t.Fatalf("%v", g)
	}
}

func TestDriftNoticeUsesEffectiveChannels(t *testing.T) {
	e := newConfigured(t)
	cfg := *e.a.Config()
	cfg.Notify = &config.Notify{Drift: []string{"console", "desktop", "webhook"}, Webhook: "https://hooks.example.com/x"}
	e.a.mu.Lock()
	e.a.cfg = &cfg
	e.a.mu.Unlock()
	var got []string
	var payload any
	e.a.Notifier.Desktop = func(_ context.Context, title, _ string) error { got = append(got, title); return nil }
	e.a.Notifier.Webhook = func(_ context.Context, url string, p any) error {
		got = append(got, url)
		payload = p
		return errors.New("down")
	}
	err := e.a.Drift.Notify(context.Background(), cfg.Drift, drift.Notice{Env: "prod", Text: "Drift in prod"})
	if err == nil || len(got) != 2 || got[0] != "groundwork: drift in prod" || got[1] != "https://hooks.example.com/x" {
		t.Fatalf("%v %v", err, got)
	}
	if n := payload.(drift.Notice); n.Project != filepath.Base(e.root) {
		t.Fatalf("%+v", n)
	}
	// an open console shows desktop notifications itself
	got = nil
	e.a.Notifier.Streams = func() int { return 1 }
	_ = e.a.Drift.Notify(context.Background(), cfg.Drift, drift.Notice{Env: "prod"})
	if len(got) != 1 {
		t.Fatalf("%v", got)
	}
}
