package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/rapando/groundwork/internal/workspace"
)

func TestRoundTrip(t *testing.T) {
	for _, repo := range []string{"iac-only", "embedded"} {
		r, err := workspace.Detect("../../testdata/repos/"+repo, nil)
		if err != nil {
			t.Fatal(err)
		}
		c := FromReport(r)
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
	c := FromReport(r)
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
	c = FromReport(r)
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

func TestUnknownFieldAndSyntax(t *testing.T) {
	if _, err := Parse([]byte("version: 1\nmode: standalone\nbogus: 1\n"), FileName); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown field: %v", err)
	}
	if _, err := Parse([]byte("version: [1\n"), FileName); err == nil {
		t.Fatal("syntax error expected")
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
