package vars

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVaultLabel(t *testing.T) {
	for ct, want := range map[string]string{
		"$ANSIBLE_VAULT;1.1;AES256\n6161\n":      "",
		"$ANSIBLE_VAULT;1.2;AES256;prod\n6161\n": "prod",
		"  $ANSIBLE_VAULT;1.2;AES256;dev\n":      "dev",
		"not a vault":                            "",
	} {
		if got := VaultLabel(ct); got != want {
			t.Errorf("VaultLabel(%q) = %q, want %q", ct, got, want)
		}
	}
}

// A !vault block ends at the next top-level line; blank lines inside it stay,
// trailing ones and the comments and keys after it are kept out.
func TestReplaceBlockKeepsTheRestOfTheFile(t *testing.T) {
	src := "a: 1\nk: !vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  6161\n\n  6262\n\n# about z\nz: 2\n"
	got := string(replaceBlock([]byte(src), 2, "k: NEW"))
	if want := "a: 1\nk: NEW\n\n# about z\nz: 2\n"; got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if got := string(replaceBlock([]byte("k: !vault |\n  x\n  y"), 1, "k: NEW")); got != "k: NEW" {
		t.Fatalf("at end of file: %q", got)
	}
}

func needVault(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ansible-vault"); err != nil {
		t.Skip("ansible-vault not installed")
	}
}

func secretNamed(t *testing.T, s *Secrets, name string) Secret {
	t.Helper()
	for _, x := range s.List(context.Background(), true) {
		if x.Name == name {
			return x
		}
	}
	t.Fatalf("no secret %s", name)
	return Secret{}
}

func revealed(t *testing.T, s *Secrets, name string) string {
	t.Helper()
	_, v, err := s.Reveal(context.Background(), secretNamed(t, s, name).ID)
	if err != nil {
		t.Fatalf("reveal %s: %v", name, err)
	}
	return v
}

// Create, edit and add vault values, then rekey them all, with real ansible-vault.
func TestVaultEditAddAndRekey(t *testing.T) {
	needVault(t)
	t.Setenv("TMPDIR", t.TempDir()) // the rekey's temporary password file must not outlive it
	root := t.TempDir()
	proj := filepath.Join(root, "ansible")
	pw := writeFile(t, root, "ansible/.vault_pass", "first-pw\n", 0o600)
	s := NewSecrets(root, []VaultProject{{Dir: proj, PasswordFile: pw}})
	ctx := context.Background()

	// a new whole vault file
	vf := filepath.Join(proj, "group_vars/prod/vault.yml")
	ct, err := s.Encrypt(ctx, vf, []byte("db_password: Alpha-111\n"), "")
	if err != nil || !strings.HasPrefix(string(ct), "$ANSIBLE_VAULT;1.1;AES256") || strings.Contains(string(ct), "Alpha") {
		t.Fatalf("%v\n%s", err, ct)
	}
	writeFile(t, root, "ansible/group_vars/prod/vault.yml", string(ct), 0o644)

	// edit it; an unchanged value isn't re-encrypted
	_, next, changed, err := s.Update(ctx, secretNamed(t, s, "vault.yml").ID, "db_password: Bravo-222\n")
	if err != nil || !changed || strings.Contains(string(next), "Bravo") {
		t.Fatalf("update: %v %v", changed, err)
	}
	writeFile(t, root, "ansible/group_vars/prod/vault.yml", string(next), 0o644)
	if v := revealed(t, s, "vault.yml"); v != "db_password: Bravo-222\n" {
		t.Fatalf("after edit: %q", v)
	}
	if _, next, changed, err := s.Update(ctx, secretNamed(t, s, "vault.yml").ID, "db_password: Bravo-222\n"); err != nil || changed || next != nil {
		t.Fatalf("same value rewrote the file: %v %v", changed, err)
	}

	// add inline values to a vars file, then edit one: the other lines stay
	main := "ansible/group_vars/prod/main.yml"
	src := "app_port: 80\n# the rest is encrypted\n"
	for _, kv := range [][2]string{{"api_token", "Charlie-333"}, {"smtp_password", "Delta-444"}} {
		b, err := s.AddInline(ctx, filepath.Join(root, main), []byte(src), kv[0], kv[1], "")
		if err != nil {
			t.Fatal(err)
		}
		src = string(b)
	}
	writeFile(t, root, main, src, 0o644)
	if _, err := s.AddInline(ctx, filepath.Join(root, main), []byte(src), "app_port", "x", ""); err == nil || !strings.Contains(err.Error(), "already set") {
		t.Fatalf("duplicate key: %v", err)
	}
	if _, err := s.AddInline(ctx, filepath.Join(root, main), []byte(src), "bad-name", "x", ""); err == nil {
		t.Fatal("invalid variable name accepted")
	}
	if _, err := s.AddInline(ctx, filepath.Join(root, main), []byte("- a\n- b\n"), "k", "x", ""); err == nil {
		t.Fatal("a YAML list accepted as a vars file")
	}
	_, next, changed, err = s.Update(ctx, secretNamed(t, s, "api_token").ID, "Echo-555")
	if err != nil || !changed {
		t.Fatalf("inline update: %v", err)
	}
	writeFile(t, root, main, string(next), 0o644)
	if !strings.HasPrefix(string(next), "app_port: 80\n# the rest is encrypted\napi_token: !vault |") || strings.Count(string(next), "!vault") != 2 {
		t.Fatalf("file around the edit changed:\n%s", next)
	}
	if revealed(t, s, "api_token") != "Echo-555" || revealed(t, s, "smtp_password") != "Delta-444" {
		t.Fatal("inline values wrong after edit")
	}

	// rekey everything in the project with a new password
	plan := s.PlanRekey(ctx, proj, "")
	if len(plan.Items) != 3 || plan.PasswordFile != pw || plan.NoUpdate != "" {
		t.Fatalf("plan %+v", plan)
	}
	if _, err := s.Rekey(ctx, plan, "", "two\nlines"); err == nil {
		t.Fatal("a multi-line password accepted")
	}
	files, err := s.Rekey(ctx, plan, "", "second-pw")
	if err != nil || len(files) != 2 {
		t.Fatalf("rekey: %v %d", err, len(files))
	}
	for _, f := range files {
		if string(f.Old) == string(f.Next) || strings.Contains(string(f.Next), "Bravo") || strings.Contains(string(f.Next), "Echo") {
			t.Fatalf("%s not re-encrypted", f.File)
		}
		writeFile(t, root, f.File, string(f.Next), 0o644)
	}
	if err := os.WriteFile(pw, []byte("second-pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s2 := NewSecrets(root, s.Projects)
	if revealed(t, s2, "vault.yml") != "db_password: Bravo-222\n" || revealed(t, s2, "api_token") != "Echo-555" || revealed(t, s2, "smtp_password") != "Delta-444" {
		t.Fatal("values changed by the rekey")
	}
	if left, _ := os.ReadDir(os.Getenv("TMPDIR")); len(left) != 0 {
		t.Fatalf("rekey left temporary files: %v", left)
	}

	// a value the password doesn't open blocks the rekey
	if err := os.WriteFile(pw, []byte("wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s3 := NewSecrets(root, s.Projects)
	plan = s3.PlanRekey(ctx, proj, "")
	if plan.Items[0].Problem == "" {
		t.Fatalf("undecryptable item not flagged: %+v", plan.Items)
	}
	if _, err := s3.Rekey(ctx, plan, "", "third-pw"); err == nil {
		t.Fatal("rekeyed values it can't decrypt")
	}
}

// With vault_identity_list in ansible.cfg, values keep their vault ID through
// edits, and a rekey touches only the ID it was asked for.
func TestVaultIDsAreKept(t *testing.T) {
	needVault(t)
	root := t.TempDir()
	proj := filepath.Join(root, "ansible")
	writeFile(t, root, "ansible/dev.pw", "dev-pw\n", 0o600)
	prodPW := writeFile(t, root, "ansible/prod.pw", "prod-pw\n", 0o600)
	cfg := writeFile(t, root, "ansible/ansible.cfg", "[defaults]\nvault_identity_list = dev@dev.pw, prod@prod.pw\n", 0o644)
	s := NewSecrets(root, []VaultProject{{Dir: proj, Config: cfg}})
	s.Environ = func() []string { return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")} }
	ctx := context.Background()

	ids := s.VaultIDs(filepath.Join(proj, "x"))
	if len(ids) != 2 || ids[0].Label != "dev" || ids[1].Label != "prod" || ids[1].Source != prodPW {
		t.Fatalf("ids %+v", ids)
	}
	vars := filepath.Join(proj, "group_vars/all.yml")
	src, err := s.AddInline(ctx, vars, nil, "dev_token", "Dev-1", "dev")
	if err != nil {
		t.Fatal(err)
	}
	src, err = s.AddInline(ctx, vars, src, "prod_token", "Prod-1", "prod")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "ansible/group_vars/all.yml", string(src), 0o644)
	if _, err := s.AddInline(ctx, vars, src, "x", "y", "-bad id"); err == nil {
		t.Fatal("invalid vault ID accepted")
	}
	if secretNamed(t, s, "prod_token").VaultID != "prod" {
		t.Fatalf("%+v", secretNamed(t, s, "prod_token"))
	}
	_, next, _, err := s.Update(ctx, secretNamed(t, s, "prod_token").ID, "Prod-2")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "ansible/group_vars/all.yml", string(next), 0o644)
	if x := secretNamed(t, s, "prod_token"); x.VaultID != "prod" || x.State != "can-decrypt" {
		t.Fatalf("edit lost the vault ID: %+v", x)
	}

	plan := s.PlanRekey(ctx, proj, "prod")
	if len(plan.Items) != 1 || plan.Items[0].Name != "prod_token" || plan.PasswordFile != prodPW {
		t.Fatalf("plan %+v", plan)
	}
	files, err := s.Rekey(ctx, plan, "prod", "prod-pw-2")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "ansible/group_vars/all.yml", string(files[0].Next), 0o644)
	writeFile(t, root, "ansible/prod.pw", "prod-pw-2\n", 0o600)
	s2 := NewSecrets(root, s.Projects)
	s2.Environ = s.Environ
	if x := secretNamed(t, s2, "prod_token"); x.VaultID != "prod" {
		t.Fatalf("rekey lost the vault ID: %+v", x)
	}
	if revealed(t, s2, "prod_token") != "Prod-2" || revealed(t, s2, "dev_token") != "Dev-1" {
		t.Fatal("values wrong after rekey")
	}
}

// A check cut short (the browser left the page, so the request's context was
// cancelled and ansible-vault killed) mustn't be remembered as "can't decrypt".
func TestCancelledCheckIsNotCached(t *testing.T) {
	root, bin := t.TempDir(), t.TempDir()
	slow := filepath.Join(bin, "slow")
	// a fake ansible-vault: hangs while the "slow" file exists, then decrypts
	writeFile(t, bin, "ansible-vault", "#!/bin/sh\n[ -e "+slow+" ] && exec sleep 10\necho 'a: 1'\n", 0o755)
	writeFile(t, bin, "slow", "", 0o644)
	pw := writeFile(t, root, "ansible/.vault_pass", "pw\n", 0o600)
	writeFile(t, root, "ansible/vault.yml", "$ANSIBLE_VAULT;1.1;AES256\n6161\n", 0o644)
	s := NewSecrets(root, []VaultProject{{Dir: filepath.Join(root, "ansible"), PasswordFile: pw}})
	s.LookPath = func(n string) (string, error) { return filepath.Join(bin, n), nil }

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	for _, x := range s.List(ctx, true) {
		if x.Name == "vault.yml" && x.State != "unchecked" {
			t.Fatalf("cancelled check reported %s: %s", x.State, x.Message)
		}
	}
	if err := os.Remove(slow); err != nil {
		t.Fatal(err)
	}
	if x := secretNamed(t, s, "vault.yml"); x.State != "can-decrypt" {
		t.Fatalf("after a cancelled check: %+v", x)
	}
}
