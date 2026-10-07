package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/store"
)

type fakeExec struct {
	have map[string]bool
	run  func(argv []string) (string, string, int)
}

func (f fakeExec) LookPath(n string) (string, error) {
	if f.have[n] {
		return "/usr/bin/" + n, nil
	}
	return "", errors.New("not found")
}
func (f fakeExec) Run(_ context.Context, _ string, argv ...string) (string, string, int, error) {
	o, e, c := f.run(argv)
	return o, e, c, nil
}

func write(t *testing.T, root, rel, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func byID(rep Report) map[string]Check {
	m := map[string]Check{}
	for _, c := range rep.Checks {
		m[c.ID] = c
	}
	return m
}

func TestDoctor(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	write(t, root, "envs/prod/backend.tf", `terraform {
  backend "s3" {
    bucket         = "acme-tfstate"
    key            = "prod/terraform.tfstate"
    dynamodb_table = "tf-locks"
  }
}`, 0o644)
	write(t, root, "envs/dev/main.tf", `terraform {
  backend "s3" {
    bucket       = "acme-dev-state"
    use_lockfile = true
  }
}`, 0o644)
	write(t, root, "ansible/group_vars/all/vault.yml", "$ANSIBLE_VAULT;1.1;AES256\n6161\n", 0o644)
	write(t, root, "ansible/.vault_pass", "pw\n", 0o644) // world-readable
	write(t, home, ".aws/sso/cache/abc.json", `{"accessToken":"x","expiresAt":"`+time.Now().Add(42*time.Minute).UTC().Format(time.RFC3339)+`"}`, 0o600)
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)

	cfg := &config.Config{
		Terraform: config.Terraform{Roots: []config.TFRoot{{Path: "envs/prod", Env: "prod"}, {Path: "envs/dev", Env: "dev"}}},
		Ansible:   config.Ansible{Projects: []config.AnsibleProject{{Path: "ansible", VaultPasswordFile: ".vault_pass", Inventories: map[string]string{"dev": "inventory/dev.yml"}}}},
		Checks:    config.Checks{Enabled: []string{"tflint", "yamllint"}},
	}
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.db"))
	defer st.Close()
	st.SetHostStatus("ansible#dev", store.HostStatus{Host: "web-1", Reachable: true, CheckedAt: time.Now()})
	st.SetHostStatus("ansible#dev", store.HostStatus{Host: "web-3", Reachable: false, CheckedAt: time.Now()})

	ex := fakeExec{
		have: map[string]bool{"terraform": true, "ansible": true, "tflint": true, "aws": true, "ssh-add": true},
		run: func(a []string) (string, string, int) {
			switch strings.Join(a[:2], " ") {
			case "terraform version":
				return "Terraform v1.16.5\non darwin_arm64", "", 0
			case "ansible --version":
				return "ansible [core 2.21.0]", "", 0
			case "tflint --version":
				return "TFLint version 0.55.0", "", 0
			case "aws sts":
				return `{"Account":"123456789012","Arn":"arn:aws:sts::123456789012:assumed-role/dev/x"}`, "", 0
			case "aws s3api":
				if a[4] == "acme-tfstate" {
					return "", "", 0
				}
				return "", "An error occurred (403) when calling the HeadBucket operation: Forbidden\n", 254
			case "aws dynamodb":
				return "{}", "", 0
			case "ssh-add -l":
				return "256 SHA256:abc dev@laptop (ED25519)\n", "", 0
			}
			t.Errorf("unexpected command %v", a)
			return "", "", 1
		},
	}
	rep := Run(context.Background(), Input{Root: root, Cfg: cfg, Providers: []string{"aws"}, Store: st, Exec: ex, Home: home})
	m := byID(rep)
	expect := func(id, status, detail string) {
		t.Helper()
		c, ok := m[id]
		if !ok {
			t.Errorf("no check %s in %+v", id, rep.Checks)
			return
		}
		if c.Status != status || !strings.Contains(c.Detail, detail) {
			t.Errorf("%s: %s %q, want %s ~%q", id, c.Status, c.Detail, status, detail)
		}
	}
	expect("tools", "ok", "terraform 1.16.5")
	expect("tool:yamllint", "warn", "yamllint check is skipped")
	expect("cred:aws", "ok", "SSO session expires in 4")
	expect("backend:s3:acme-tfstate", "ok", "reachable")
	expect("lock:s3:acme-tfstate", "ok", "tf-locks")
	expect("backend:s3:acme-dev-state", "fail", "Forbidden")
	expect("lock:s3:acme-dev-state", "ok", "native")
	expect("ssh-agent", "ok", "1 key loaded · ed25519")
	expect("reach:ansible#dev", "warn", "1 of 2 hosts answer ping · down: web-3")
	expect("vault:ansible", "warn", "readable by other users")
	expect("disk", "ok", "free")
	if _, ok := m["git"]; ok {
		t.Error("git repo present: no warning expected")
	}
}

func TestDoctorFailures(t *testing.T) {
	root := t.TempDir()
	write(t, root, "ansible/site.yml", "- hosts: all\n  vars: { x: !vault |\n      $ANSIBLE_VAULT;1.1;AES256\n      61 }\n", 0o644)
	cfg := &config.Config{
		Terraform: config.Terraform{Roots: []config.TFRoot{{Path: "tf", Env: "dev"}}},
		Ansible:   config.Ansible{Projects: []config.AnsibleProject{{Path: "ansible"}}},
	}
	ex := fakeExec{have: map[string]bool{"aws": true, "ssh-add": true}, run: func(a []string) (string, string, int) {
		switch a[0] {
		case "aws":
			return "", "\nError when retrieving token from sso: Token has expired and refresh failed\n", 255
		case "ssh-add":
			return "", "Could not open a connection to your authentication agent.", 2
		}
		return "", "", 1
	}}
	t.Setenv("AWS_PROFILE", "staging")
	t.Setenv("ANSIBLE_VAULT_PASSWORD_FILE", "")
	m := byID(Run(context.Background(), Input{Root: root, Cfg: cfg, Providers: []string{"aws"}, Exec: ex, Home: t.TempDir()}))
	if c := m["tool:terraform"]; c.Status != "fail" || c.Hint == "" {
		t.Errorf("%+v", c)
	}
	if c := m["cred:aws"]; c.Status != "fail" || c.Hint != "aws sso login --profile staging" {
		t.Errorf("%+v", c)
	}
	if c := m["ssh-agent"]; c.Status != "warn" || !strings.Contains(c.Detail, "no SSH agent") {
		t.Errorf("%+v", c)
	}
	if c := m["vault:ansible"]; c.Status != "fail" {
		t.Errorf("%+v", c)
	}
	if c := m["git"]; c.Status != "warn" {
		t.Errorf("%+v", c)
	}
}
