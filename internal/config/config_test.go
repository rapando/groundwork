package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rapando/groundwork/v2/internal/workspace"
)

func TestRoundTrip(t *testing.T) {
	for _, repo := range []string{"iac-only", "embedded"} {
		r, err := workspace.Detect("../../testdata/repos/"+repo, nil)
		if err != nil {
			t.Fatal(err)
		}
		c := FromReport(r, "terraform")
		b, err := c.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(b, FileName); err != nil {
			t.Fatalf("%s: generated config invalid: %v\n%s", repo, err, b)
		}
	}
}

func TestFromReportShapes(t *testing.T) {
	r, _ := workspace.Detect("../../testdata/repos/embedded", nil)
	c := FromReport(r, "terraform")
	if c.Mode != "embedded" || len(c.Terraform.Roots) != 1 {
		t.Fatalf("%+v", c)
	}
	root := c.Terraform.Roots[0]
	if root.Path != "deploy/terraform" || len(root.Envs) != 2 ||
		root.Envs["prod"].VarFiles[0] != "env/production.tfvars" || root.Envs["prod"].Workspace != "prod" {
		t.Fatalf("workspace root wrong: %+v", root)
	}
	if got := c.Ansible.Projects[0].Inventories["dev"]; got != "inventory/dev.yml" {
		t.Fatalf("inventory %q", got)
	}

	r, _ = workspace.Detect("../../testdata/repos/iac-only", nil)
	c = FromReport(r, "terraform")
	if c.Mode != "standalone" || c.Terraform.Roots[1].Env != "prod" ||
		len(c.Terraform.Modules) != 1 || c.Terraform.Modules[0] != "terraform/modules/*" {
		t.Fatalf("%+v", c)
	}
}

func TestValidationPositions(t *testing.T) {
	src := `version: 1
mode: standalone
terraform:
  roots:
    - path: a
      env: dev
    - path: ../escape
      env: prod
      approval: sometimes
checks:
  enabled: [fmt, bogus]
`
	_, err := Parse([]byte(src), FileName)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("got %v", err)
	}
	want := map[string]int{
		"terraform.roots[1].path":     7,
		"terraform.roots[1].approval": 9,
		"checks.enabled[1]":           11,
	}
	for _, p := range ve.Problems {
		if l, ok := want[p.Path]; ok {
			if p.Line != l {
				t.Errorf("%s: line %d want %d", p.Path, p.Line, l)
			}
			delete(want, p.Path)
		}
	}
	if len(want) > 0 {
		t.Fatalf("missing problems %v in %v", want, err)
	}
	if !strings.Contains(err.Error(), "groundwork.yaml:7:") {
		t.Fatalf("error text lacks position: %v", err)
	}
}

// An unknown top-level key is what a newer release's config looks like to
// this one: it opens, with a warning. Unknown keys inside a known block are
// typos and stay errors.
func TestUnknownFieldAndSyntax(t *testing.T) {
	c, err := Parse([]byte("version: 1\nmode: standalone\nbogus: 1\n"), FileName)
	if err != nil || c == nil || len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], `"bogus"`) || !strings.Contains(c.Warnings[0], "groundwork.yaml:3") {
		t.Fatalf("unknown top-level field should warn, not fail: %v %v", err, c)
	}
	if c.Mode != "standalone" {
		t.Fatal("the rest of the file must still be read")
	}
	if _, err := Parse([]byte("version: 1\nmode: standalone\nterraform:\n  bogus: 1\n"), FileName); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown nested field: %v", err)
	}
	if _, err := Parse([]byte("version: [1\n"), FileName); err == nil {
		t.Fatal("syntax error expected")
	}
}

func TestRetentionAndRunTimeoutDefaults(t *testing.T) {
	base := "version: 1\nmode: standalone\n"
	c, err := Parse([]byte(base), FileName)
	if err != nil || c.Retention.RunsOrDefault() != 500 || c.Retention.MaxAge() != 30*24*time.Hour ||
		c.Terraform.RunTimeoutDuration() != 2*time.Hour || c.Ansible.RunTimeoutDuration() != 2*time.Hour {
		t.Fatalf("defaults: %v %+v", err, c)
	}
	c, err = Parse([]byte(base+"retention: { runs: 50, days: 7 }\nterraform: { run_timeout: 45m }\nansible: { run_timeout: 3h }\n"), FileName)
	if err != nil || c.Retention.RunsOrDefault() != 50 || c.Retention.MaxAge() != 7*24*time.Hour ||
		c.Terraform.RunTimeoutDuration() != 45*time.Minute || c.Ansible.RunTimeoutDuration() != 3*time.Hour {
		t.Fatalf("configured: %v %+v", err, c)
	}
	for _, bad := range []string{"retention: { runs: -1 }\n", "terraform: { run_timeout: soon }\n", "ansible: { run_timeout: -1h }\n"} {
		if _, err := Parse([]byte(base+bad), FileName); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestApprovalDefaults(t *testing.T) {
	r := TFRoot{Env: "prod"}
	if !r.ApprovalRequired("prod") || r.ApprovalRequired("dev") {
		t.Fatal("prod-like default")
	}
	r = TFRoot{Approval: "none"}
	if r.ApprovalRequired("prod") {
		t.Fatal("explicit none must win")
	}
}

func TestMarshalStartsWithDocumentMarker(t *testing.T) {
	// yamllint (default rules) flags files without "---"; groundwork.yaml can sit inside a linted Ansible project.
	c := &Config{Version: 1, Mode: "standalone"}
	b, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\n---\n") {
		t.Fatalf("missing document start:\n%s", b)
	}
	if _, err := Parse(b, FileName); err != nil {
		t.Fatal(err)
	}
}

func TestPlanTTLAndDangerValidation(t *testing.T) {
	base := "version: 1\nmode: standalone\nterraform:\n  roots:\n    - path: a\n      env: dev\n"
	c, err := Parse([]byte(base), FileName)
	if err != nil || c.Terraform.PlanTTLDuration().String() != "1h0m0s" {
		t.Fatalf("default ttl: %v %v", err, c)
	}
	c, err = Parse([]byte(base+"  plan_ttl: 20m\n  danger: [\"*_cache*\"]\n"), FileName)
	if err != nil || c.Terraform.PlanTTLDuration().Minutes() != 20 || c.Terraform.Danger[0] != "*_cache*" {
		t.Fatalf("%v %+v", err, c)
	}
	for _, bad := range []string{"  plan_ttl: soon\n", "  plan_ttl: -5m\n", "  danger: [\"[\"]\n"} {
		if _, err := Parse([]byte(base+bad), FileName); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// Everything in groundwork.yaml that reaches a subprocess or the filesystem is
// validated: the file is part of the repository, so a cloned repository
// controls it.
func TestRepoControlledValuesAreValidated(t *testing.T) {
	base := "version: 1\nmode: standalone\n"
	good := []string{
		"terraform:\n  roots:\n    - path: a\n      envs: { dev: { workspace: dev-1 }, prod.eu: {} }\n",
		"ansible:\n  projects:\n    - path: a\n      config: ansible.cfg\n      vault_password_file: .vault\n",
		"ansible:\n  projects:\n    - path: a\n      vault_password_file: ~/.vault\n",
		"ansible:\n  projects:\n    - path: a\n      vault_password_file: /etc/vault-pass\n",
		"drift: { schedule: '0 * * * *', notify: webhook, webhook: https://hooks.example.com/x }\n",
		"ignore: ['vendor/', '*.bak']\n",
	}
	for _, g := range good {
		if _, err := Parse([]byte(base+g), FileName); err != nil {
			t.Errorf("rejected %q: %v", g, err)
		}
	}
	bad := map[string]string{
		"terraform:\n  roots:\n    - path: a\n      envs: { dev: { workspace: '-chdir=/' } }\n": "terraform.roots[0].envs.dev.workspace",
		"terraform:\n  roots:\n    - path: a\n      envs: { 'de v': {} }\n":                     "terraform.roots[0].envs.de v",
		"terraform:\n  roots:\n    - path: a\n      env: 'a/b'\n":                               "terraform.roots[0].env",
		"ansible:\n  projects:\n    - path: a\n      config: ../ansible.cfg\n":                  "ansible.projects[0].config",
		"ansible:\n  projects:\n    - path: a\n      vault_password_file: ../../x\n":            "ansible.projects[0].vault_password_file",
		"ansible:\n  projects:\n    - path: a\n      vault_password_file: ~root/x\n":            "ansible.projects[0].vault_password_file",
		"ansible:\n  projects:\n    - path: a\n      inventories: { 'd;v': inv }\n":             "ansible.projects[0].inventories.d;v",
		"drift: { notify: webhook, webhook: 'file:///etc/passwd' }\n":                           "drift.webhook",
		"drift: { notify: webhook, webhook: 'https://user:pw@h/x' }\n":                          "drift.webhook",
		"drift: { notify: webhook, webhook: 'not a url' }\n":                                    "drift.webhook",
		"ignore: ['']\n": "ignore[0]",
	}
	for src, path := range bad {
		_, err := Parse([]byte(base+src), FileName)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("accepted %q", src)
			continue
		}
		found := false
		for _, p := range ve.Problems {
			found = found || p.Path == path
		}
		if !found {
			t.Errorf("%q: want a problem at %s, got %v", src, path, err)
		}
	}
}

// ansible executes a vault password file that has the execute bit set, so a
// repository can't point groundwork at a script unless the user opts in.
func TestVaultFileRefusesExecutables(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "ans"), 0o755)
	os.WriteFile(filepath.Join(root, "ans", "plain"), []byte("pw\n"), 0o600)
	os.WriteFile(filepath.Join(root, "ans", "script"), []byte("#!/bin/sh\necho pw\n"), 0o700)
	c := &Config{}
	p := AnsibleProject{Path: "ans", VaultPasswordFile: "plain"}
	if got, err := c.VaultFile(root, p); err != nil || got != filepath.Join(root, "ans", "plain") {
		t.Fatalf("plain: %q %v", got, err)
	}
	p.VaultPasswordFile = "script"
	if _, err := c.VaultFile(root, p); err == nil || !strings.Contains(err.Error(), "allow_vault_script") {
		t.Fatalf("executable accepted: %v", err)
	}
	c.Ansible.AllowVaultScript = true
	if got, err := c.VaultFile(root, p); err != nil || got != filepath.Join(root, "ans", "script") {
		t.Fatalf("opted in: %q %v", got, err)
	}
	// missing files are the doctor's business, not an error here
	p.VaultPasswordFile = "nope"
	if _, err := (&Config{}).VaultFile(root, p); err != nil {
		t.Fatal(err)
	}
	if got, _ := (&Config{}).VaultFile(root, AnsibleProject{}); got != "" {
		t.Fatal("no file should stay empty")
	}
}
