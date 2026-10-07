package diagnostics

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rapando/groundwork/internal/checks"
)

func scopeOf(id string) string {
	if strings.HasPrefix(id, "ans-") {
		return "ansible"
	}
	return "terraform"
}

// Every rule fires on its own fixture, and fixtures don't trip unrelated rules.
func TestEveryRuleFiresOnItsFixture(t *testing.T) {
	rules, errs := LoadRules("")
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(rules) < 20 {
		t.Fatalf("only %d rules", len(rules))
	}
	// other rules a fixture legitimately triggers too
	extra := map[string][]string{
		"ans-become-password": {"ans-python-interpreter"}, // the real capture has both
	}
	ds := lintFindings(t)
	for _, r := range rules {
		t.Run(r.ID, func(t *testing.T) {
			var ids []string
			var hit *Hit
			if len(r.diagRes) > 0 && len(r.logRes) == 0 {
				for _, h := range MatchDiagnostics(rules, ds) {
					if h.Rule.ID == r.ID {
						hit = &h
					}
				}
				if hit == nil {
					t.Fatalf("no finding in the lint fixtures triggers %s", r.ID)
				}
			} else {
				b, err := os.ReadFile(filepath.Join("testdata", "logs", r.ID+".log"))
				if err != nil {
					t.Fatalf("every rule needs a fixture: %v", err)
				}
				for _, h := range MatchLog(rules, scopeOf(r.ID), string(b)) {
					ids = append(ids, h.Rule.ID)
					if h.Rule.ID == r.ID && hit == nil {
						hh := h
						hit = &hh
					}
				}
				want := append([]string{r.ID}, extra[r.ID]...)
				sort.Strings(ids)
				sort.Strings(want)
				if strings.Join(dedupe(ids), ",") != strings.Join(want, ",") {
					t.Fatalf("fixture triggers %v, want %v", ids, want)
				}
			}
			ctx := Context{Target: "envs/dev (dev)", Root: "envs/dev", Env: "dev", Project: "ansible", RunID: 7, RunKind: "tf.plan"}
			v := Render(r, *hit, ctx)
			for _, s := range append([]string{v.Title, v.Explain}, stepTexts(v)...) {
				if strings.Contains(s, "<no value>") || strings.Contains(s, "{{") {
					t.Errorf("unrendered template: %q", s)
				}
			}
			t.Logf("%s — %s", v.Title, v.Explain)
		})
	}
}

func dedupe(s []string) []string {
	out := s[:0]
	for i, x := range s {
		if i == 0 || x != s[i-1] {
			out = append(out, x)
		}
	}
	return out
}

func stepTexts(v View) []string {
	var out []string
	for _, s := range v.Steps {
		out = append(out, s.Title, s.Detail)
		if s.Action != nil {
			out = append(out, s.Action.Label, s.Action.Href, s.Action.Text, s.Action.Command)
		}
	}
	return out
}

func lintFindings(t *testing.T) []checks.Diagnostic {
	b, err := os.ReadFile("../../testdata/checks/ansible_lint.json")
	if err != nil {
		t.Fatal(err)
	}
	ds, err := checks.ParseAnsibleLint(b, "ansible")
	if err != nil {
		t.Fatal(err)
	}
	y, _ := os.ReadFile("testdata/diags/yamllint-indentation.txt")
	return append(ds, checks.ParseYamllint(y, "ansible")...)
}

func TestExtractedValues(t *testing.T) {
	rules, _ := LoadRules("")
	get := func(id, scope string) []Hit {
		b, _ := os.ReadFile(filepath.Join("testdata", "logs", id+".log"))
		var out []Hit
		for _, h := range MatchLog(rules, scope, string(b)) {
			if h.Rule.ID == id {
				out = append(out, h)
			}
		}
		return out
	}
	lock := get("tf-state-lock-stale", "terraform")[0]
	if lock.Vars["lock_id"] != "f1e1e064-b652-a5d3-66a4-7c5053b5f0f2" || lock.Vars["operation"] != "OperationTypeApply" || lock.Vars["who"] != "dev@workstation.local" {
		t.Errorf("%v", lock.Vars)
	}
	un := get("tf-unsupported-argument", "terraform")[0]
	if un.Vars["argument"] != "triggers_replac" || un.Vars["suggestion"] != "triggers_replace" || un.Vars["file"] != "main.tf" || un.Vars["line"] != "2" {
		t.Errorf("%v", un.Vars)
	}
	if h := get("ans-unreachable-timeout", "ansible"); len(h) != 1 || h[0].Key != "web-03" || h[0].Vars["address"] != "10.255.255.1" {
		t.Errorf("%+v", h)
	}
	if h := get("ans-become-password", "ansible"); len(h) != 1 || h[0].Key != "local-1" {
		t.Errorf("%+v", h)
	}
	if h := get("ans-vault-decrypt-failed", "ansible"); h[0].Vars["file"] != "/home/dev/infra/ansible/group_vars/all.yml" {
		t.Errorf("%v", h[0].Vars)
	}
	if h := get("tf-module-source-not-found", "terraform"); h[0].Vars["where"] != "main.tf:1" {
		t.Errorf("%v", h[0].Vars)
	}
	if h := get("aws-sso-expired", "terraform"); len(h) != 1 || h[0].Vars["profile"] != "" {
		t.Errorf("%+v", h)
	}
	// scope: terraform rules don't run on ansible logs
	b, _ := os.ReadFile("testdata/logs/tf-state-lock-stale.log")
	if len(MatchLog(rules, "ansible", string(b))) != 0 {
		t.Error("scope ignored")
	}
}

func TestDiagnosticHitLinksToTheFile(t *testing.T) {
	rules, _ := LoadRules("")
	b, _ := os.ReadFile("../../testdata/checks/validate_errors.json")
	ds, err := checks.ParseValidate(b, "terraform/envs/dev")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range MatchDiagnostics(rules, ds) {
		if h.Rule.ID != "tf-unsupported-argument" {
			continue
		}
		v := Render(h.Rule, h, Context{Target: h.Vars["file"]})
		if !strings.HasPrefix(v.Steps[0].Action.Href, "/code/terraform/envs/dev/") || strings.Contains(v.Steps[0].Action.Href, "//") {
			t.Fatalf("%q", v.Steps[0].Action.Href)
		}
		return
	}
	t.Fatal("validate's Unsupported argument finding didn't match")
}

func TestUserRulesOverrideAndExtend(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "mine.yaml"), []byte(`
- id: tf-missing-variable
  scope: terraform
  match: { any: [ { log: 'No value for required variable' } ] }
  title: 'Ask the platform team about {{ .variable }}'
  extract: { variable: 'input variable "([^"]+)"' }
- id: corp-proxy
  scope: any
  match: { any: [ { log: 'proxyconnect tcp' } ] }
  title: Corporate proxy refused the connection
`), 0o644)
	os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("- id: x\n  match: {any: [{log: '('}]}\n  title: t\n"), 0o644)
	rules, errs := LoadRules(dir)
	if len(errs) != 1 {
		t.Fatalf("broken file should be reported: %v", errs)
	}
	b, _ := os.ReadFile("testdata/logs/tf-missing-variable.log")
	h := MatchLog(rules, "terraform", string(b))
	if len(h) != 1 || Render(h[0].Rule, h[0], Context{}).Title != "Ask the platform team about alert_email" {
		t.Fatalf("%+v", h)
	}
	if len(MatchLog(rules, "terraform", "dial: proxyconnect tcp: refused")) != 1 {
		t.Fatal("user rule not loaded")
	}
}
