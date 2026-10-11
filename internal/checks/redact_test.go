package checks

import (
	"context"
	"strings"
	"testing"
)

// Linter findings are stored and shown in the Problems tray, so what a tool
// echoes is masked like run output: registered environment secrets and
// credential-looking assignments.
func TestFindingsAreRedacted(t *testing.T) {
	t.Setenv("CI_DEPLOY_TOKEN", "deploy-token-value-123")
	fx := &fakeExec{}
	fx.handler = func(c Cmd) Output {
		j := strings.Join(c.Argv, " ")
		switch {
		case strings.Contains(j, " validate "):
			return Output{Stdout: []byte(`{"valid":false,"error_count":1,"warning_count":0,"diagnostics":[
				{"severity":"error","summary":"Invalid value password = \"abc12345xyz\" for provider",
				 "detail":"Authentication with deploy-token-value-123 failed","range":{"filename":"main.tf","start":{"line":1,"column":1},"end":{"line":1,"column":2}}}]}`), ExitCode: 1}
		case strings.Contains(j, " fmt "), strings.HasPrefix(j, "tflint"):
			return Output{Stdout: []byte(`{"issues":[],"errors":[]}`)}
		case strings.HasPrefix(j, "yamllint"):
			return Output{Stdout: []byte("roles/nginx/tasks/main.yml:1:1: [warning] token: deploy-token-value-123 (secret)\n")}
		case strings.HasPrefix(j, "ansible-lint"):
			return Output{Stdout: []byte("[]")}
		}
		return Output{}
	}
	e := newEnv(t, fx)
	if err := e.svc.Run(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	ds, err := e.svc.Diagnostics("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, d := range ds {
		seen[d.Tool] = true
		for _, text := range []string{d.Message, d.Detail} {
			if strings.Contains(text, "deploy-token-value-123") || strings.Contains(text, "abc12345xyz") {
				t.Errorf("%s finding leaks a secret: %q", d.Tool, text)
			}
		}
	}
	if !seen["validate"] || !seen["yamllint"] {
		t.Fatalf("expected validate and yamllint findings, got %v", seen)
	}
}
