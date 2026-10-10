package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
)

func newInventoryRepo(t *testing.T) (string, *testEnv) {
	t.Helper()
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(s), 0o644)
	}
	write("groundwork.yaml", "version: 1\nmode: standalone\nansible:\n  projects:\n    - path: .\n      inventories:\n        dev: environments/dev/hosts.yml\n")
	write("environments/dev/hosts.yml", "# dev\np2p:\n  children:\n    dev:\n      hosts:\n        p2p-dev:\n          ansible_host: 10.0.0.1\nlocal:\n  hosts: {}\n")
	write("environments/dev/group_vars/dev/main.yml", "---\ndeploy_env: dev\n")
	write("environments/dev/group_vars/dev/vault.yml", "$ANSIBLE_VAULT;1.1;AES256\n6162\n")
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.db"))
	t.Cleanup(func() { st.Close() })
	a := New(root, "test", events.NewBus(), st)
	t.Cleanup(a.Checks.Close)
	h := chi.NewRouter()
	a.Routes(h)
	return root, &testEnv{a: a, h: h, root: root}
}

func TestInventoryEditPreviewApplyAndConflict(t *testing.T) {
	root, e := newInventoryRepo(t)
	inv := filepath.Join(root, "environments/dev/hosts.yml")
	before, _ := os.ReadFile(inv)
	body := map[string]any{"project": ".", "env": "dev", "op": "add_host", "host": "laptop", "group": "local"}

	pv := into(t, call(t, e.h, "POST", "/inventory/edit", body))
	if pv["file"] != "environments/dev/hosts.yml" || !strings.Contains(pv["diff"].(string), "+    laptop:") {
		t.Fatalf("%v", pv)
	}
	if now, _ := os.ReadFile(inv); string(now) != string(before) {
		t.Fatal("a preview must not write")
	}

	stale := map[string]any{}
	for k, v := range body {
		stale[k] = v
	}
	stale["apply"], stale["sha"] = true, "not-the-sha"
	if rec := call(t, e.h, "POST", "/inventory/edit", stale); rec.Code != 409 {
		t.Fatalf("stale sha: %d %s", rec.Code, rec.Body)
	}

	body["apply"], body["sha"] = true, pv["sha"]
	if rec := call(t, e.h, "POST", "/inventory/edit", body); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	now, _ := os.ReadFile(inv)
	if want := strings.Replace(string(before), "  hosts: {}\n", "  hosts:\n    laptop:\n", 1); string(now) != want {
		t.Fatalf("got:\n%s", now)
	}
}

func TestInventoryVarTargetsAndVarsFiles(t *testing.T) {
	root, e := newInventoryRepo(t)
	targets := func(scope, name string) []string {
		var out []string
		for _, x := range into(t, call(t, e.h, "GET", "/inventory/var-targets?project=.&env=dev&scope="+scope+"&name="+name, nil))["targets"].([]any) {
			m := x.(map[string]any)
			out = append(out, m["file"].(string)+map[bool]string{true: "", false: " (new)"}[m["exists"].(bool)])
		}
		return out
	}
	// the vault file is never a target; an existing readable file is
	if got := strings.Join(targets("group", "dev"), ","); got != "environments/dev/hosts.yml,environments/dev/group_vars/dev/main.yml" {
		t.Fatalf("group dev: %s", got)
	}
	if got := strings.Join(targets("host", "p2p-dev"), ","); got != "environments/dev/hosts.yml,environments/dev/host_vars/p2p-dev.yml (new)" {
		t.Fatalf("host: %s", got)
	}
	if got := strings.Join(targets("host", "../../etc"), ","); got != "environments/dev/hosts.yml" {
		t.Fatalf("a bad name gets no vars file: %s", got)
	}

	set := func(extra map[string]any) map[string]any {
		b := map[string]any{"project": ".", "env": "dev", "op": "set_var", "scope": "host", "host": "p2p-dev", "key": "app_port", "value": "8080", "file": "environments/dev/host_vars/p2p-dev.yml"}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	pv := into(t, call(t, e.h, "POST", "/inventory/edit", set(nil)))
	if pv["create"] != true {
		t.Fatalf("%v", pv)
	}
	if rec := call(t, e.h, "POST", "/inventory/edit", set(map[string]any{"apply": true, "sha": ""})); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "environments/dev/host_vars/p2p-dev.yml")); string(b) != "---\napp_port: 8080\n" {
		t.Fatalf("new host_vars: %q", b)
	}

	for _, c := range []struct {
		body map[string]any
		code int
	}{
		{set(map[string]any{"key": "db_password", "value": "hunter2"}), 422},
		{set(map[string]any{"file": "environments/dev/group_vars/dev/vault.yml"}), 400},
		{set(map[string]any{"file": "../outside.yml"}), 400},
		{set(map[string]any{"scope": "nope"}), 400},
		{map[string]any{"project": ".", "env": "dev", "op": "add_host", "host": "x", "group": "nope"}, 422},
	} {
		if rec := call(t, e.h, "POST", "/inventory/edit", c.body); rec.Code != c.code {
			t.Errorf("%v: want %d, got %d %s", c.body, c.code, rec.Code, rec.Body)
		}
	}
}
