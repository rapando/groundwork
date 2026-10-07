package api

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
)

func TestTerraformVarsMatrixEditAndReveal(t *testing.T) {
	root := fixture(t, "vars-lab")
	dbPath := filepath.Join(t.TempDir(), "s.db")
	st, _ := store.Open(dbPath)
	defer st.Close()
	h := chi.NewRouter()
	New(root, "test", events.NewBus(), st).Routes(h)
	if rec := call(t, h, "POST", "/setup/apply", map[string]any{}); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	m := into(t, call(t, h, "GET", "/vars/terraform", nil))
	if m["stack"] != "envs/*" {
		t.Fatalf("stack %v (%v)", m["stack"], m["stacks"])
	}
	rows := map[string]map[string]any{}
	for _, r := range m["rows"].([]any) {
		rows[r.(map[string]any)["name"].(string)] = r.(map[string]any)
	}
	rep := rows["replicas"]["cells"].([]any)
	if rep[0].(map[string]any)["env"] != "dev" || rep[0].(map[string]any)["value"] != "3" || rep[1].(map[string]any)["value"] != "6" {
		t.Fatalf("replicas %v", rep)
	}
	if !strings.Contains(fmtJSON(m), "••") || strings.Contains(fmtJSON(m), "s3cret") {
		t.Fatal("sensitive values must be masked in the matrix")
	}

	// edit: preview, then apply with the sha; a sensitive variable is refused
	body := map[string]any{"root": "envs/prod", "env": "prod", "name": "alert_email", "value": `"ops@example.com"`, "file": "envs/prod/terraform.tfvars"}
	pv := into(t, call(t, h, "PUT", "/vars/terraform", body))
	if !strings.Contains(pv["diff"].(string), `+alert_email`) {
		t.Fatalf("%v", pv)
	}
	body["apply"], body["sha"] = true, "stale"
	if rec := call(t, h, "PUT", "/vars/terraform", body); rec.Code != 409 {
		t.Fatalf("stale sha: %d", rec.Code)
	}
	body["sha"] = pv["sha"]
	if rec := call(t, h, "PUT", "/vars/terraform", body); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	b, _ := os.ReadFile(filepath.Join(root, "envs/prod/terraform.tfvars"))
	if !strings.Contains(string(b), `alert_email = "ops@example.com"`) {
		t.Fatalf("%s", b)
	}
	for _, bad := range []map[string]any{
		{"root": "envs/prod", "env": "prod", "name": "db_password", "value": `"x"`, "file": "envs/prod/terraform.tfvars"},
		{"root": "envs/prod", "env": "prod", "name": "region", "value": `"x"`, "file": "../../etc/passwd"},
		{"root": "envs/prod", "env": "prod", "name": "region", "value": `var.x`, "file": "envs/prod/terraform.tfvars"},
	} {
		if rec := call(t, h, "PUT", "/vars/terraform", bad); rec.Code < 400 {
			t.Errorf("%v accepted: %s", bad, rec.Body)
		}
	}

	// reveal: value in the response only, never in the DB
	rec := call(t, h, "POST", "/secrets/reveal", map[string]any{"root": "envs/dev", "env": "dev", "name": "db_password"})
	if rec.Code != 200 || into(t, rec)["value"] != `"s3cret-dev-pw"` || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, rec.Header())
	}
	sl := into(t, call(t, h, "GET", "/secrets", nil))
	if rv := sl["reveals"].([]any); len(rv) != 1 || rv[0].(map[string]any)["name"] != "db_password" {
		t.Fatalf("audit %v", rv)
	}
	assertNotOnDisk(t, filepath.Dir(dbPath), "s3cret-dev-pw")
	assertNotOnDisk(t, filepath.Join(root, ".groundwork"), "s3cret-dev-pw")

	// plaintext scan finds the committed passwords
	sc := into(t, call(t, h, "POST", "/secrets/scan", nil))
	if n := len(sc["findings"].([]any)); n != 2 || strings.Contains(fmtJSON(sc), "s3cret") {
		t.Fatalf("findings %v", sc)
	}
}

func TestMoveToVaultAndRevealWithRealAnsible(t *testing.T) {
	if _, err := exec.LookPath("ansible-vault"); err != nil {
		t.Skip("ansible-vault not installed")
	}
	root := fixture(t, "ansible-lab")
	if err := os.WriteFile(filepath.Join(root, ".vault_pass"), []byte("pw-for-tests\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gv := filepath.Join(root, "inventory/group_vars/all.yml")
	orig, _ := os.ReadFile(gv)
	if err := os.WriteFile(gv, append(orig, []byte("smtp_password: Mail-Pw-9931x\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	line := bytes.Count(orig, []byte("\n")) + 1
	dbPath := filepath.Join(t.TempDir(), "s.db")
	st, _ := store.Open(dbPath)
	defer st.Close()
	h := chi.NewRouter()
	New(root, "test", events.NewBus(), st).Routes(h)
	if rec := call(t, h, "POST", "/setup/apply", map[string]any{}); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	cfgPath := filepath.Join(root, "groundwork.yaml")
	cfg, _ := os.ReadFile(cfgPath)
	cfg = bytes.Replace(cfg, []byte("    - path: .\n"), []byte("    - path: .\n      vault_password_file: .vault_pass\n"), 1)
	if err := os.WriteFile(cfgPath, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	h = chi.NewRouter() // a fresh API loads the edited config
	New(root, "test", events.NewBus(), st).Routes(h)

	findings := into(t, call(t, h, "POST", "/secrets/scan", nil))["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("%v", findings)
	}
	f := findings[0].(map[string]any)
	if f["file"] != "inventory/group_vars/all.yml" || int(f["line"].(float64)) != line {
		t.Fatalf("%v", f)
	}
	pv := into(t, call(t, h, "POST", "/secrets/move", map[string]any{"file": f["file"], "line": line}))
	if strings.Contains(fmtJSON(pv), "Mail-Pw") || !strings.Contains(pv["diff"].(string), "!vault") {
		t.Fatalf("preview leaks or is wrong: %v", pv)
	}
	rec := call(t, h, "POST", "/secrets/move", map[string]any{"file": f["file"], "line": line, "apply": true, "sha": pv["sha"]})
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	after, _ := os.ReadFile(gv)
	if bytes.Contains(after, []byte("Mail-Pw")) || !bytes.Contains(after, []byte("smtp_password: !vault |")) {
		t.Fatalf("%s", after)
	}
	if n := len(into(t, call(t, h, "POST", "/secrets/scan", nil))["findings"].([]any)); n != 0 {
		t.Fatalf("still flagged: %d", n)
	}

	var id string
	for _, s := range into(t, call(t, h, "GET", "/secrets", nil))["secrets"].([]any) {
		sm := s.(map[string]any)
		if sm["name"] == "smtp_password" {
			if sm["state"] != "can-decrypt" {
				t.Fatalf("%v", sm)
			}
			id = sm["id"].(string)
		}
	}
	rec = call(t, h, "POST", "/secrets/reveal", map[string]any{"id": id})
	if rec.Code != 200 || into(t, rec)["value"] != "Mail-Pw-9931x" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	assertNotOnDisk(t, filepath.Dir(dbPath), "Mail-Pw-9931x")
	assertNotOnDisk(t, root, "Mail-Pw-9931x")
}

func assertNotOnDisk(t *testing.T, dir, secret string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(secret)) {
			t.Errorf("%s contains the revealed secret", p)
		}
		return nil
	})
}

func fmtJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
