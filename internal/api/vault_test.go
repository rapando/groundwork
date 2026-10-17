package api

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
)

// vaultLab is the ansible-lab fixture with a vault password configured.
func vaultLab(t *testing.T) (root string, h http.Handler, dbDir string) {
	t.Helper()
	if _, err := exec.LookPath("ansible-vault"); err != nil {
		t.Skip("ansible-vault not installed")
	}
	root = fixture(t, "ansible-lab")
	if err := os.WriteFile(filepath.Join(root, ".vault_pass"), []byte("pw-one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbDir = t.TempDir()
	st, _ := store.Open(filepath.Join(dbDir, "s.db"))
	t.Cleanup(func() { st.Close() })
	r := chi.NewRouter()
	New(root, "test", events.NewBus(), st).Routes(r)
	if rec := call(t, r, "POST", "/setup/apply", map[string]any{}); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	cfgPath := filepath.Join(root, "groundwork.yaml")
	cfg, _ := os.ReadFile(cfgPath)
	cfg = bytes.Replace(cfg, []byte("    - path: .\n"), []byte("    - path: .\n      vault_password_file: .vault_pass\n"), 1)
	if err := os.WriteFile(cfgPath, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	r = chi.NewRouter() // a fresh API loads the edited config
	New(root, "test", events.NewBus(), st).Routes(r)
	return root, r, dbDir
}

func secretByName(t *testing.T, h http.Handler, name string) map[string]any {
	t.Helper()
	for _, s := range into(t, call(t, h, "GET", "/secrets", nil))["secrets"].([]any) {
		if sm := s.(map[string]any); sm["name"] == name {
			return sm
		}
	}
	t.Fatalf("no secret %s", name)
	return nil
}

func revealAPI(t *testing.T, h http.Handler, name string) (value, sha string) {
	t.Helper()
	rec := call(t, h, "POST", "/secrets/reveal", map[string]any{"id": secretByName(t, h, name)["id"]})
	if rec.Code != 200 {
		t.Fatalf("reveal %s: %d %s", name, rec.Code, rec.Body)
	}
	m := into(t, rec)
	sha, _ = m["sha"].(string)
	return m["value"].(string), sha
}

func TestVaultCreateEditAddEncryptAndRekey(t *testing.T) {
	root, h, dbDir := vaultLab(t)
	vf := "inventory/group_vars/prod/vault.yml"

	// create a vault file; creating it again is refused
	rec := call(t, h, "POST", "/secrets/vault/create", map[string]any{"file": vf, "value": "db_password: Create-101\n"})
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(root, vf)); !bytes.HasPrefix(b, []byte("$ANSIBLE_VAULT;")) {
		t.Fatalf("not encrypted: %s", b)
	}
	if rec := call(t, h, "POST", "/secrets/vault/create", map[string]any{"file": vf, "value": "x"}); rec.Code != 409 {
		t.Fatalf("overwrote an existing file: %d", rec.Code)
	}
	list := into(t, call(t, h, "GET", "/secrets", nil))
	if v := fmtJSON(list["vault"]); !strings.Contains(v, `"path":"."`) || !strings.Contains(v, `"label":"default"`) {
		t.Fatalf("vault projects %s", v)
	}

	// edit: a stale sha is refused, the same value changes nothing
	v, sha := revealAPI(t, h, "vault.yml")
	if v != "db_password: Create-101\n" || sha == "" {
		t.Fatalf("%q %q", v, sha)
	}
	id := secretByName(t, h, "vault.yml")["id"]
	if rec := call(t, h, "POST", "/secrets/vault/update", map[string]any{"id": id, "value": "db_password: Edit-202\n", "sha": "stale"}); rec.Code != 409 {
		t.Fatalf("stale sha: %d %s", rec.Code, rec.Body)
	}
	if m := into(t, call(t, h, "POST", "/secrets/vault/update", map[string]any{"id": id, "value": v, "sha": sha})); m["changed"] != false {
		t.Fatalf("%v", m)
	}
	if m := into(t, call(t, h, "POST", "/secrets/vault/update", map[string]any{"id": id, "value": "db_password: Edit-202\n", "sha": sha})); m["changed"] != true {
		t.Fatalf("%v", m)
	}
	if v, _ := revealAPI(t, h, "vault.yml"); v != "db_password: Edit-202\n" {
		t.Fatalf("after edit %q", v)
	}

	// add an inline value; duplicates and non-YAML files are refused
	all := "inventory/group_vars/all.yml"
	if rec := call(t, h, "POST", "/secrets/vault/add", map[string]any{"file": all, "key": "api_token", "value": "Add-303"}); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, bad := range []map[string]any{
		{"file": all, "key": "api_token", "value": "x"},
		{"file": "inventory/notes.txt", "key": "k", "value": "x"},
		{"file": vf, "key": "k", "value": "x"},
		{"file": all, "key": "k", "value": "x", "vault_id": "--vault-password-file=/tmp/x"},
	} {
		if rec := call(t, h, "POST", "/secrets/vault/add", bad); rec.Code < 400 {
			t.Errorf("%v accepted", bad)
		}
	}
	if v, _ := revealAPI(t, h, "api_token"); v != "Add-303" {
		t.Fatalf("%q", v)
	}

	// encrypt a plaintext file in place
	plain := "inventory/group_vars/web.yml"
	if rec := call(t, h, "POST", "/secrets/vault/encrypt", map[string]any{"file": plain}); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, "POST", "/secrets/vault/encrypt", map[string]any{"file": plain}); rec.Code != 422 {
		t.Fatalf("encrypted twice: %d", rec.Code)
	}

	// rekey: the preview lists everything, apply re-encrypts it and writes the new password
	pv := into(t, call(t, h, "POST", "/secrets/vault/rekey", map[string]any{"project": "."}))
	if n := len(pv["items"].([]any)); n != 3 || pv["password_file"] != filepath.Join(root, ".vault_pass") {
		t.Fatalf("preview %v", pv)
	}
	if rec := call(t, h, "POST", "/secrets/vault/rekey", map[string]any{"project": "../..", "apply": true, "password": "x"}); rec.Code != 404 {
		t.Fatalf("unknown project: %d", rec.Code)
	}
	before, _ := os.ReadFile(filepath.Join(root, all))
	rec = call(t, h, "POST", "/secrets/vault/rekey", map[string]any{"project": ".", "password": "pw-two", "update_password_file": true, "apply": true})
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if pw, _ := os.ReadFile(filepath.Join(root, ".vault_pass")); string(pw) != "pw-two\n" {
		t.Fatalf("password file %q", pw)
	}
	if after, _ := os.ReadFile(filepath.Join(root, all)); bytes.Equal(before, after) {
		t.Fatal("not re-encrypted")
	}
	if v, _ := revealAPI(t, h, "vault.yml"); v != "db_password: Edit-202\n" {
		t.Fatalf("after rekey %q", v)
	}
	if v, _ := revealAPI(t, h, "api_token"); v != "Add-303" {
		t.Fatalf("after rekey %q", v)
	}

	for _, s := range []string{"Create-101", "Edit-202", "Add-303", "pw-two"} {
		assertNotOnDisk(t, dbDir, s)
		assertNotOnDisk(t, filepath.Join(root, ".groundwork"), s)
	}
}

// When one file of a rekey can't be written, the files already written and
// the password file are put back: the old password still opens everything.
func TestVaultRekeyRollsBack(t *testing.T) {
	root, h, _ := vaultLab(t)
	for f, key := range map[string]string{"inventory/group_vars/all.yml": "all_token", "inventory/host_vars/web-1.yml": "web_token"} {
		if rec := call(t, h, "POST", "/secrets/vault/add", map[string]any{"file": f, "key": key, "value": "v"}); rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	allBefore, _ := os.ReadFile(filepath.Join(root, "inventory/group_vars/all.yml"))
	locked := filepath.Join(root, "inventory/host_vars")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	rec := call(t, h, "POST", "/secrets/vault/rekey", map[string]any{"project": ".", "password": "pw-new", "update_password_file": true, "apply": true})
	if rec.Code < 400 || !strings.Contains(rec.Body.String(), "host_vars") {
		t.Fatalf("expected the write into host_vars (after group_vars/all.yml) to fail: %d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "inventory/group_vars/all.yml")); !bytes.Equal(b, allBefore) {
		t.Fatal("a written file wasn't restored")
	}
	if pw, _ := os.ReadFile(filepath.Join(root, ".vault_pass")); string(pw) != "pw-one\n" {
		t.Fatalf("password file not restored: %q", pw)
	}
}
