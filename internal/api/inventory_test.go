package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
)

func TestInventoryOverHTTPWithRealAnsible(t *testing.T) {
	if _, err := exec.LookPath("ansible-inventory"); err != nil {
		t.Skip("ansible not installed")
	}
	root := fixture(t, "ansible-lab")
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.db"))
	defer st.Close()
	a := New(root, "test", events.NewBus(), st)
	h := chi.NewRouter()
	a.Routes(h)
	if rec := call(t, h, "POST", "/setup/apply", map[string]any{}); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	e := &testEnv{a: a, h: h, root: root}

	inv := into(t, call(t, h, "GET", "/inventory", nil))
	if inv["error"] != nil {
		t.Fatalf("%v", inv["error"])
	}
	scope := inv["scope"].(map[string]any)
	if scope["project"] != "." || scope["env"] != "dev" {
		t.Fatalf("%v", scope)
	}
	if len(inv["hosts"].([]any)) != 4 {
		t.Fatalf("%v", inv["hosts"])
	}
	files := map[string]bool{}
	for _, f := range inv["files"].([]any) {
		files[f.(string)] = true
	}
	for _, want := range []string{"inventory/dev.yml", "inventory/group_vars/web.yml", "inventory/host_vars/web-1.yml", "group_vars/web.yml"} {
		if !files[want] {
			t.Errorf("files missing %s: %v", want, files)
		}
	}
	if pbs := inv["playbooks"].([]any); len(pbs) != 1 || pbs[0] != "site.yml" {
		t.Fatalf("%v", pbs)
	}

	// ping through the runs API, then the inventory shows reachability
	rec := call(t, h, "POST", "/runs", map[string]any{"kind": "ans.ping", "project": ".", "env": "dev"})
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	waitRun(t, e, into(t, rec)["run"].(map[string]any)["id"].(float64), "succeeded")
	inv = into(t, call(t, h, "GET", "/inventory?project=.&env=dev", nil))
	reach := map[string]bool{}
	for _, x := range inv["hosts"].([]any) {
		m := x.(map[string]any)
		reach[m["name"].(string)] = m["reachable"].(map[string]any)["reachable"].(bool)
	}
	if !reach["web-1"] || reach["down-1"] {
		t.Fatalf("%v", reach)
	}
	for _, g := range inv["groups"].([]any) {
		m := g.(map[string]any)
		if m["name"] == "flaky" && m["down"].(float64) != 1 {
			t.Fatalf("group down count: %v", m)
		}
	}

	host := into(t, call(t, h, "GET", "/inventory/host?project=.&env=dev&host=web-2", nil))
	var nw map[string]any
	for _, r := range host["vars"].(map[string]any)["rows"].([]any) {
		if r.(map[string]any)["name"] == "nginx_workers" {
			nw = r.(map[string]any)
		}
	}
	if nw["value"] != "3" || nw["status"] != "ok" || nw["winner"].(map[string]any)["file"] != "group_vars/web.yml" {
		t.Fatalf("%v", nw)
	}
	if call(t, h, "GET", "/inventory/host?project=.&env=dev&host=nope", nil).Code != 404 {
		t.Fatal("unknown host")
	}
	if rec := call(t, h, "GET", "/inventory?project=.&env=nope", nil); into(t, rec)["error"] == nil {
		t.Fatal("unknown env should report an error")
	}
}

// Encrypted group_vars: the inventory needs the project's vault password
// file, and says which setting to fix when there is none.
func TestInventoryDecryptsVaultedGroupVars(t *testing.T) {
	if _, err := exec.LookPath("ansible-vault"); err != nil {
		t.Skip("ansible not installed")
	}
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(s), 0o600)
	}
	write("site.yml", "- hosts: dev\n  gather_facts: false\n  tasks:\n    - ansible.builtin.ping:\n")
	write("environments/dev/hosts.yml", "p2p:\n  children:\n    dev:\n      hosts:\n        dev-1:\n          ansible_connection: local\n")
	write("environments/dev/group_vars/dev/vault.yml", "vault_secret: s3cret\n")
	pw := filepath.Join(t.TempDir(), "vault-pass")
	os.WriteFile(pw, []byte("pw\n"), 0o600)
	if out, err := exec.Command("ansible-vault", "encrypt", "--vault-password-file="+pw, filepath.Join(root, "environments/dev/group_vars/dev/vault.yml")).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	cfg := "version: 1\nmode: standalone\nansible:\n  projects:\n    - path: .\n      inventories:\n        dev: environments/dev/hosts.yml\n"
	write("groundwork.yaml", cfg)

	st, _ := store.Open(filepath.Join(t.TempDir(), "s.db"))
	defer st.Close()
	a := New(root, "test", events.NewBus(), st)
	t.Cleanup(a.Checks.Close)
	h := chi.NewRouter()
	a.Routes(h)

	inv := into(t, call(t, h, "GET", "/inventory", nil))
	if msg, _ := inv["error"].(string); !strings.Contains(msg, "Set vault_password_file for Ansible project . in groundwork.yaml") {
		t.Fatalf("want a vault hint, got %v", inv["error"])
	}

	write("groundwork.yaml", strings.Replace(cfg, "    - path: .\n", "    - path: .\n      vault_password_file: "+pw+"\n", 1))
	a.reloadConfig()
	inv = into(t, call(t, h, "GET", "/inventory", nil))
	if inv["error"] != nil {
		t.Fatalf("%v", inv["error"])
	}
	if hosts := inv["hosts"].([]any); len(hosts) != 1 {
		t.Fatalf("%v", hosts)
	}
	if pbs := inv["playbooks"].([]any); len(pbs) != 1 || pbs[0] != "site.yml" {
		t.Fatalf("%v", pbs)
	}
	host := into(t, call(t, h, "GET", "/inventory/host?project=.&env=dev&host=dev-1", nil))
	if host["error"] != nil {
		t.Fatalf("%v", host["error"])
	}
}
