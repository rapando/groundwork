package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rapando/groundwork/v2/internal/store"
)

// A variable declared sensitive is masked by terraform in its own output, but
// a provider, a linter or a stage error can still echo the value. The job's
// redactor is seeded with the values the target gives such variables, from
// tfvars, -var-file files and TF_VAR_*; log, stage detail and run error are
// all checked.
func TestSensitiveVariableValuesAreMaskedEverywhere(t *testing.T) {
	g := newRig(t)
	dir := filepath.Join(g.root, "terraform", "ws")
	os.MkdirAll(filepath.Join(dir, "env"), 0o755)
	os.WriteFile(filepath.Join(dir, "variables.tf"), []byte(`
variable "db_password" {
  type      = string
  sensitive = true
}
variable "api_key" {
  type      = string
  sensitive = true
  default   = "default-key-value-9"
}
variable "token" {
  type      = string
  sensitive = true
}
variable "region" {
  type = string
}
`), 0o644)
	os.WriteFile(filepath.Join(dir, "terraform.tfvars"), []byte("region = \"eu-west-1\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "env", "prod.tfvars"), []byte("db_password = \"tfvars-pw-value-1\"\n"), 0o644)
	t.Setenv("TF_VAR_token", "env-token-value-2")

	script := filepath.Join(t.TempDir(), "bin")
	os.MkdirAll(script, 0o755)
	os.WriteFile(filepath.Join(script, "terraform"), []byte("#!/bin/sh\n"+
		"echo \"connecting with tfvars-pw-value-1 default-key-value-9 env-token-value-2 in eu-west-1\"\n"+
		"echo \"Error: login failed for tfvars-pw-value-1 (default-key-value-9, env-token-value-2)\" >&2\nexit 1\n"), 0o755)
	t.Setenv("PATH", script+string(os.PathListSeparator)+os.Getenv("PATH"))

	run, err := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/ws", Env: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	d := g.wait(t, run.ID, store.StatusFailed)
	raw, _ := os.ReadFile(g.r.logPath(run.ID))
	sinks := map[string]string{"log": string(raw), "summary error": d.Summary.Error}
	for _, s := range d.Stages {
		sinks["stage "+s.Name] = s.Detail
	}
	for name, text := range sinks {
		for _, secret := range []string{"tfvars-pw-value-1", "default-key-value-9", "env-token-value-2"} {
			if strings.Contains(text, secret) {
				t.Errorf("%s leaks %s: %s", name, secret, text)
			}
		}
	}
	if !strings.Contains(string(raw), "eu-west-1") {
		t.Fatalf("a non-sensitive value was masked too: %s", raw)
	}
}
