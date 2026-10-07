package vars

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

const sopsYAML = `db_password: ENC[AES256_GCM,data:Zm9v,iv:aXY=,tag:dGFn,type:str]
api_token: ENC[AES256_GCM,data:YmFy,iv:aXY=,tag:dGFn,type:str]
sops:
    age:
        - recipient: age1xyz
    lastmodified: "2026-10-07T00:00:00Z"
    mac: ENC[AES256_GCM,data:bWFj,iv:aXY=,tag:dGFn,type:str]
    version: 3.9.0
`

// a fake sops: --decrypt prints a known plaintext; --extract one key.
const fakeSops = `#!/bin/sh
case "$*" in
  *--extract*db_password*) printf 'sops-db-pw\n' ;;
  *--decrypt*) printf 'db_password: sops-db-pw\napi_token: t\n' ;;
  *) exit 1 ;;
esac
`

func TestSecretsDiscoveryAndSopsReveal(t *testing.T) {
	root := t.TempDir()
	bin := t.TempDir()
	writeFile(t, bin, "sops", fakeSops, 0o755)
	writeFile(t, root, "secrets/prod.enc.yaml", sopsYAML, 0o644)
	writeFile(t, root, "ansible/group_vars/all/vault.yml", "$ANSIBLE_VAULT;1.1;AES256\n6161\n", 0o644)
	writeFile(t, root, "ansible/group_vars/all/main.yml", "app_port: 80\napi_key: !vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  6161\n", 0o644)
	writeFile(t, root, ".terraform/x.yml", "a: !vault |\n  x\n", 0o644)

	s := NewSecrets(root, []VaultProject{{Dir: filepath.Join(root, "ansible")}})
	s.Environ = func() []string { return []string{"AWS_PROFILE=dev", "HOME=/x", "PATH=" + bin + ":/usr/bin:/bin"} }
	s.LookPath = func(n string) (string, error) {
		if n == "sops" {
			return filepath.Join(bin, "sops"), nil
		}
		return "", exec.ErrNotFound
	}
	list := s.List(context.Background(), true)
	byName := map[string]Secret{}
	for _, x := range list {
		byName[x.Kind+":"+x.Name] = x
	}
	if len(list) != 5 {
		t.Fatalf("got %d secrets: %+v", len(list), list)
	}
	if x := byName["sops:db_password"]; x.State != "can-decrypt" || !x.Revealable || x.Line != 1 || x.File != "secrets/prod.enc.yaml" {
		t.Errorf("sops %+v", x)
	}
	if x := byName["vault-inline:api_key"]; x.State != "tool-missing" || x.Revealable || x.Line != 2 {
		t.Errorf("inline %+v", x)
	}
	if x := byName["vault-file:vault.yml"]; x.State != "tool-missing" {
		t.Errorf("vault file %+v", x)
	}
	if x := byName["env:AWS_PROFILE"]; x.State != "names-only" || x.Revealable || strings.Contains(x.Message, "dev") {
		t.Errorf("env %+v", x)
	}
	_, v, err := s.Reveal(context.Background(), byName["sops:db_password"].ID)
	if err != nil || v != "sops-db-pw" {
		t.Fatalf("reveal %q %v", v, err)
	}
	if _, _, err := s.Reveal(context.Background(), byName["env:AWS_PROFILE"].ID); err == nil {
		t.Fatal("env vars must not be revealable")
	}
	if _, _, err := s.Reveal(context.Background(), "sops\x1f../../etc/passwd\x1fx"); err == nil {
		t.Fatal("unknown ids must be refused")
	}
}

// Real ansible-vault round trip: encrypt_string → inline value → reveal.
func TestVaultRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("ansible-vault"); err != nil {
		t.Skip("ansible-vault not installed")
	}
	root := t.TempDir()
	proj := filepath.Join(root, "ansible")
	pw := writeFile(t, root, "ansible/.vault_pass", "correct-horse\n", 0o600)
	vars := writeFile(t, root, "ansible/group_vars/all.yml", "app_port: 80\ndb_password: Plain-Text-Pw1\n", 0o644)
	s := NewSecrets(root, []VaultProject{{Dir: proj, PasswordFile: pw}})
	ctx := context.Background()

	src, _ := os.ReadFile(vars)
	key, val, err := PlainAssignment(src, 2)
	if err != nil || key != "db_password" || val != "Plain-Text-Pw1" {
		t.Fatalf("%s %s %v", key, val, err)
	}
	block, err := s.EncryptString(ctx, vars, key, val)
	if err != nil || !strings.HasPrefix(block, "db_password: !vault |") || strings.Contains(block, "Plain-Text") {
		t.Fatalf("%v\n%s", err, block)
	}
	if err := os.WriteFile(vars, ReplaceLine(src, 2, block), 0o644); err != nil {
		t.Fatal(err)
	}
	var sec Secret
	for _, x := range s.List(ctx, true) {
		if x.Name == "db_password" {
			sec = x
		}
	}
	if sec.State != "can-decrypt" {
		t.Fatalf("%+v", sec)
	}
	if _, v, err := s.Reveal(ctx, sec.ID); err != nil || v != "Plain-Text-Pw1" {
		t.Fatalf("reveal %q %v", v, err)
	}
	if err := os.WriteFile(pw, []byte("wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s2 := NewSecrets(root, s.Projects)
	for _, x := range s2.List(ctx, true) {
		if x.Name == "db_password" && x.State != "wrong-password" {
			t.Fatalf("wrong password: %+v", x)
		}
	}
	// no password configured at all
	s3 := NewSecrets(root, []VaultProject{{Dir: proj}})
	s3.Environ = func() []string { return []string{"PATH=" + os.Getenv("PATH")} }
	for _, x := range s3.List(ctx, true) {
		if x.Name == "db_password" && x.State != "no-password" {
			t.Fatalf("no password: %+v", x)
		}
	}
}

func TestPlainAssignment(t *testing.T) {
	src := []byte("a: 1\n  nested: x\nb: \"{{ v }}\"\nc: |\n  multi\nd: 'quoted pw'\n")
	for line, ok := range map[int]bool{1: true, 2: false, 3: false, 4: false, 6: true, 99: false} {
		_, _, err := PlainAssignment(src, line)
		if (err == nil) != ok {
			t.Errorf("line %d: %v", line, err)
		}
	}
	if _, v, _ := PlainAssignment(src, 6); v != "quoted pw" {
		t.Errorf("got %q", v)
	}
}
