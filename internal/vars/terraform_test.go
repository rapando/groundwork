package vars

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func lab(t *testing.T) (string, []Target) {
	t.Helper()
	repo, _ := filepath.Abs("../../testdata/repos/vars-lab")
	return repo, []Target{{Root: "envs/dev", Env: "dev"}, {Root: "envs/prod", Env: "prod"}}
}

func rows(t *testing.T, environ []string) map[string]Row {
	repo, targets := lab(t)
	out := map[string]Row{}
	for _, r := range Matrix(repo, targets, environ) {
		out[r.Name] = r
	}
	return out
}

func TestMatrixFollowsTerraformPrecedence(t *testing.T) {
	m := rows(t, nil)
	reg := m["region"]
	if reg.Cells[0].State != "default" || reg.Cells[0].Value != `"eu-west-1"` || reg.Cells[1].Value != `"us-east-1"` || reg.Cells[1].Source != "terraform.tfvars" || !reg.Differs {
		t.Fatalf("region: %+v", reg)
	}
	rep := m["replicas"]
	if rep.Cells[0].Value != "3" || rep.Cells[0].Source != "override.auto.tfvars" || rep.Cells[0].File != "envs/dev/override.auto.tfvars" || rep.Cells[0].Line != 2 {
		t.Fatalf("*.auto.tfvars beats terraform.tfvars: %+v", rep.Cells[0])
	}
	if rep.Decl.Description != "How many app servers." || len(rep.Decl.Validation) != 1 || rep.Decl.Validation[0] != "var.replicas > 0" || rep.Decl.Type != "number" {
		t.Fatalf("decl: %+v", rep.Decl)
	}
	if !strings.Contains(m["tags"].Cells[0].Value, `"team":"core"`) {
		t.Fatalf("map default: %+v", m["tags"].Cells[0])
	}
}

func TestSensitiveMissingUnused(t *testing.T) {
	m := rows(t, nil)
	db := m["db_password"]
	b, _ := json.Marshal(db)
	if strings.Contains(string(b), "s3cret") || db.Cells[0].State != "secret" || db.Cells[0].Value != Masked || !db.Sensitive {
		t.Fatalf("sensitive values must never be in the matrix: %s", b)
	}
	// alert_email defaults to null in code; dev sets it, prod leaves it null
	if a := m["alert_email"]; a.Cells[1].State != "default" || a.Cells[1].Value != "null" {
		t.Fatalf("%+v", a.Cells[1])
	}
	if !m["unused_flag"].Unused || m["region"].Unused {
		t.Fatal("unused detection")
	}
	if len(m["region"].Decl.UsedIn) != 1 || m["region"].Decl.UsedIn[0].File != "envs/dev/main.tf" {
		t.Fatalf("used in: %+v", m["region"].Decl.UsedIn)
	}
}

func TestEnvironmentRanksAboveDefaultsBelowTfvars(t *testing.T) {
	m := rows(t, []string{"TF_VAR_region=ap-south-1", "TF_VAR_replicas=9"})
	if c := m["region"].Cells[0]; c.State != "env" || c.Value != Masked || !strings.Contains(c.Source, "TF_VAR_region") {
		t.Fatalf("env beats the default: %+v", c)
	}
	if c := m["region"].Cells[1]; c.Value != `"us-east-1"` {
		t.Fatalf("tfvars beat the environment: %+v", c)
	}
	if c := m["replicas"].Cells[1]; c.Value != "6" {
		t.Fatalf("%+v", c)
	}
}

func TestMissingRequired(t *testing.T) {
	repo, _ := lab(t)
	d := &Decl{Name: "needed", Type: "string"}
	c := Resolve(repo, Target{Root: "envs/prod", Env: "prod"}, d, nil)
	if c.State != "missing" {
		t.Fatalf("%+v", c)
	}
	if strings.Join(c.Writable, ",") != "envs/prod/terraform.tfvars" {
		t.Fatalf("writable %v", c.Writable)
	}
	w := Resolve(repo, Target{Root: "envs/dev", Env: "dev", VarFiles: []string{"envs/dev/extra.tfvars"}}, d, nil).Writable
	if strings.Join(w, ",") != "envs/dev/extra.tfvars,envs/dev/terraform.tfvars,envs/dev/override.auto.tfvars" {
		t.Fatalf("writable %v", w)
	}
}

func TestRawValueForReveal(t *testing.T) {
	repo, targets := lab(t)
	decls := Declarations(repo, filepath.Join(repo, "envs/dev"))
	v, err := RawValue(repo, targets[0], decls["db_password"])
	if err != nil || v != `"s3cret-dev-pw"` {
		t.Fatalf("%q %v", v, err)
	}
}
