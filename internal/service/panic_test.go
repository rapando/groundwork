package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// One project's job panicking is that project's problem: its run fails with
// the reason, an issue is raised there, and every project keeps serving.
func TestPanicInOneProjectLeavesTheOthersServing(t *testing.T) {
	e := newEnv(t)
	add := func() string {
		dir := repo(t)
		os.WriteFile(filepath.Join(dir, "groundwork.yaml"), []byte("version: 1\nmode: standalone\nterraform:\n  roots:\n    - path: terraform/envs/dev\n      env: dev\n"), 0o644)
		resp, p := e.call(t, "POST", "/api/projects", map[string]string{"path": dir}, true)
		if resp.StatusCode != 201 {
			t.Fatalf("add: %d %v", resp.StatusCode, p)
		}
		return p["id"].(string)
	}
	a, b := add(), add()

	e.svc.mu.Lock()
	e.svc.live[a].api.Runner.Hooks.BeforeStart = func(string) { panic("simulated bug in project a") }
	e.svc.mu.Unlock()

	resp, run := e.call(t, "POST", "/api/p/"+a+"/runs", map[string]any{"kind": "tf.plan", "root": "terraform/envs/dev", "env": "dev"}, true)
	if resp.StatusCode != 202 && resp.StatusCode != 201 && resp.StatusCode != 200 {
		t.Fatalf("submit: %d %v", resp.StatusCode, run)
	}
	id := int64(run["run"].(map[string]any)["id"].(float64))
	var status string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, d := e.call(t, "GET", "/api/p/"+a+"/runs/"+json.Number(itoa(id)).String(), nil, true)
		if r, ok := d["run"].(map[string]any); ok {
			status, _ = r["status"].(string)
			if status == "failed" {
				if b, _ := json.Marshal(d); !strings.Contains(string(b), "internal error") {
					t.Fatalf("run should say why: %s", b)
				}
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != "failed" {
		t.Fatalf("run status %q, want failed", status)
	}
	resp, _ = e.call(t, "GET", "/api/p/"+a+"/issues", nil, true)
	if resp.StatusCode != 200 {
		t.Fatalf("issues on a: %d", resp.StatusCode)
	}
	if b, _ := json.Marshal(issuesOf(t, e, a)); !strings.Contains(string(b), "groundwork-internal-error") {
		t.Fatalf("no internal-error issue on project a: %s", b)
	}
	for _, id := range []string{a, b} {
		if resp, _ := e.call(t, "GET", "/api/p/"+id+"/workspace", nil, true); resp.StatusCode != 200 {
			t.Fatalf("project %s stopped serving: %d", id, resp.StatusCode)
		}
	}
	if resp, _ := e.call(t, "GET", "/api/projects", nil, true); resp.StatusCode != 200 {
		t.Fatalf("service stopped serving: %d", resp.StatusCode)
	}
}

func issuesOf(t *testing.T, e *env, id string) any {
	t.Helper()
	_, out := e.call(t, "GET", "/api/p/"+id+"/issues", nil, true)
	return out
}

func itoa(n int64) string { return json.Number(strconvItoa(n)).String() }

func strconvItoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
