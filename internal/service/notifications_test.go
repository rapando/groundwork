package service

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rapando/groundwork/v2/internal/store"
)

func TestNotificationsAcrossProjects(t *testing.T) {
	e := newEnv(t)
	var ids []string
	for i := 0; i < 2; i++ {
		resp, p := e.call(t, "POST", "/api/projects", map[string]string{"path": repo(t)}, true)
		if resp.StatusCode != 201 {
			t.Fatalf("add: %d %v", resp.StatusCode, p)
		}
		ids = append(ids, p["id"].(string))
	}
	e.svc.mu.Lock()
	a, b := e.svc.live[ids[0]], e.svc.live[ids[1]]
	e.svc.mu.Unlock()
	base := time.Now().Add(-time.Hour)
	add := func(in *instance, run int64, ev string, at time.Duration) {
		n := store.Notification{Event: ev, RunID: run, Title: "x", At: base.Add(at)}
		if _, err := in.store.AddNotification(&n); err != nil {
			t.Fatal(err)
		}
	}
	add(a, 1, store.EventFailed, 0)
	add(b, 1, store.EventSucceeded, time.Minute)
	add(a, 2, store.EventApproval, 2*time.Minute)
	if _, err := a.store.CreateRun("tf.plan", map[string]any{}, []string{}, ""); err != nil {
		t.Fatal(err)
	}
	r, _ := a.store.CreateRun("tf.plan", map[string]any{}, []string{}, "")
	_ = a.store.UpdateRun(r.ID, store.RunUpdate{Status: store.StatusWaitingApproval})

	resp, l := e.call(t, "GET", "/api/notifications?limit=2", nil, true)
	if resp.StatusCode != 200 || l["unread"].(float64) != 3 {
		t.Fatalf("%d %v", resp.StatusCode, l)
	}
	items := l["items"].([]any)
	first := items[0].(map[string]any)
	if len(items) != 2 || first["project"] != ids[0] || first["project_name"] != a.p.Name || first["event"] != "approval" ||
		items[1].(map[string]any)["project"] != ids[1] {
		t.Fatalf("%v", items)
	}

	views := e.svc.views()
	for _, v := range views {
		want := [3]int{1, 0, 0}
		if v.ID == ids[0] {
			want = [3]int{2, 1, 1}
		}
		if got := [3]int{v.UnreadNotifications, v.UnreadFailed, v.WaitingApproval}; got != want {
			t.Errorf("%s: %v want %v", v.ID, got, want)
		}
	}

	if resp, m := e.call(t, "POST", "/api/notifications/read", map[string]any{"project": ids[0]}, true); resp.StatusCode != 200 || m["unread"].(float64) != 1 {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if resp, _ := e.call(t, "POST", "/api/notifications/read", map[string]any{"project": "nope"}, true); resp.StatusCode != 404 {
		t.Fatal(resp.StatusCode)
	}
	if resp, _ := e.call(t, "POST", "/api/notifications/read", map[string]any{}, true); resp.StatusCode != 400 {
		t.Fatal(resp.StatusCode)
	}
	if resp, _ := e.call(t, "POST", "/api/notifications/read", map[string]any{"bogus": 1}, true); resp.StatusCode != 400 {
		t.Fatal(resp.StatusCode)
	}
	if resp, m := e.call(t, "POST", "/api/notifications/read", map[string]any{"all": true}, true); resp.StatusCode != 200 || m["unread"].(float64) != 0 {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if resp, _ := e.call(t, "GET", "/api/notifications", nil, false); resp.StatusCode != 401 {
		t.Fatal("needs credentials")
	}
}

// The settings for every project live in <home>/notify.yaml; each project
// resolves its own over them, and a hand edit applies without a restart.
func TestNotificationSettingsForEveryProject(t *testing.T) {
	e := newEnv(t)
	resp, p := e.call(t, "POST", "/api/projects", map[string]string{"path": repo(t)}, true)
	if resp.StatusCode != 201 {
		t.Fatalf("add: %d %v", resp.StatusCode, p)
	}
	id := p["id"].(string)

	_, g := e.call(t, "GET", "/api/notifications/settings", nil, true)
	if g["configured"] != false || g["settings"].(map[string]any)["quiet_under"] != "30s" || g["path"] != NotifyPath(e.svc.Home) {
		t.Fatalf("%v", g)
	}
	body := map[string]any{"failed": []string{"console", "webhook"}, "approval": []string{"console"}, "success": []string{},
		"drift": []string{"console"}, "quiet_under": "2m", "envs": []string{}, "webhook": "https://hooks.example.com/all"}
	if resp, g = e.call(t, "PUT", "/api/notifications/settings", body, true); resp.StatusCode != 200 || g["configured"] != true {
		t.Fatalf("%d %v", resp.StatusCode, g)
	}
	b, err := os.ReadFile(NotifyPath(e.svc.Home))
	if err != nil || !strings.Contains(string(b), "notify:\n  failed: [console, webhook]\n") {
		t.Fatalf("%v\n%s", err, b)
	}
	_, g = e.call(t, "GET", "/api/p/"+id+"/notifications/settings", nil, true)
	if st := g["settings"].(map[string]any); st["webhook"] != "https://hooks.example.com/all" || st["quiet_under"] != "2m" || g["shared"] != true {
		t.Fatalf("project doesn't inherit: %v", g)
	}

	for _, bad := range []map[string]any{{"failed": []string{"pager"}}, {"failed": []string{"webhook"}, "webhook": ""}, {"extra": 1}} {
		if resp, _ := e.call(t, "PUT", "/api/notifications/settings", bad, true); resp.StatusCode != 400 {
			t.Errorf("%v: %d", bad, resp.StatusCode)
		}
	}
	if b2, _ := os.ReadFile(NotifyPath(e.svc.Home)); string(b2) != string(b) {
		t.Fatal("a refused save changed the file")
	}

	// a hand edit applies; a broken one is reported and ignored
	if err := os.WriteFile(NotifyPath(e.svc.Home), []byte("notify:\n  quiet_under: 5m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := e.svc.MachineNotify(); n == nil || n.QuietUnder != "5m" {
		t.Fatalf("%+v", n)
	}
	if err := os.WriteFile(NotifyPath(e.svc.Home), []byte("notify:\n  failed: [sms]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, g = e.call(t, "GET", "/api/notifications/settings", nil, true); g["error"] == nil || !strings.Contains(g["error"].(string), "unknown channel") || e.svc.MachineNotify() != nil {
		t.Fatalf("%v", g)
	}

	os.Remove(NotifyPath(e.svc.Home))
	if resp, g = e.call(t, "POST", "/api/notifications/test", nil, true); resp.StatusCode != 200 || len(g["sent"].([]any)) != 1 {
		t.Fatalf("test send by the defaults: %d %v", resp.StatusCode, g)
	}
}
