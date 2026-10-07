//go:build integration

package checks

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
	"github.com/rapando/groundwork/internal/workspace"
)

// Runs the real terraform binary (built-in terraform_data resource: no cloud,
// no provider downloads). Skipped when terraform is not on PATH.
func TestRealTerraformValidateInitAndFix(t *testing.T) {
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform not installed")
	}
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(s), 0o644)
	}
	write("terraform/modules/box/main.tf", "variable \"in\" {\n  type = string\n}\n\nresource \"terraform_data\" \"x\" {\n  input = var.in\n}\n")
	write("terraform/envs/dev/main.tf", `provider "terraform" {}

module "box" {
  source = "../../modules/box"
  in     = "hi"
}

resource "terraform_data" "a" {
  input           = "x"
  triggers_replac = ["y"]
}
 resource "terraform_data" "ugly" {
 input   =   1
}
`)
	write("terraform/envs/dev/backend.tf", "terraform {\n  backend \"local\" {}\n}\n")

	rep, _ := workspace.Detect(root, nil)
	cfg := config.FromReport(rep)
	cfg.Checks.Enabled = []string{"fmt", "validate"}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(root, func() *config.Config { return cfg }, OSExec{}, st, events.NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	start := time.Now()
	if err := svc.Run(context.Background(), "tf:terraform/envs/dev", false); err != nil {
		t.Fatal(err)
	}
	t.Logf("first run (includes init): %s", time.Since(start))
	ds, _ := svc.Diagnostics("tf:terraform/envs/dev", "", "")
	var gotFix, gotFmt bool
	for _, d := range ds {
		t.Logf("%s %s %s:%d:%d %s", d.Tool, d.Severity, d.File, d.Line, d.Col, d.Message)
		if d.Tool == "validate" && d.Fix != nil && d.Fix.Edits[0].New == "triggers_replace" {
			gotFix = true
			if d.File != "terraform/envs/dev/main.tf" || d.Line != 10 {
				t.Errorf("wrong location %+v", d)
			}
		}
		if d.Tool == "validate" && strings.Contains(d.Message, "Module not installed") {
			t.Error("init-needed error leaked as a diagnostic")
		}
		if d.Tool == "fmt" && d.File == "terraform/envs/dev/main.tf" {
			gotFmt = true
		}
	}
	if !gotFix || !gotFmt {
		t.Fatalf("fix=%v fmt=%v in %+v", gotFix, gotFmt, ds)
	}

	// the repo itself must be untouched by validate: no .terraform dir, no state
	for _, p := range []string{"terraform/envs/dev/.terraform", "terraform/envs/dev/terraform.tfstate", "terraform/envs/dev/.terraform.lock.hcl"} {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			t.Errorf("validate polluted the repo: %s exists", p)
		}
	}

	// apply the edit fix exactly as the UI does (verify Old, replace), then the fmt fix
	b, _ := os.ReadFile(filepath.Join(root, "terraform/envs/dev/main.tf"))
	lines := strings.Split(string(b), "\n")
	for _, d := range ds {
		if d.Fix != nil && d.Fix.Kind == "edit" {
			e := d.Fix.Edits[0]
			l := lines[e.Line-1]
			if got := l[e.Col-1 : e.EndCol-1]; got != e.Old {
				t.Fatalf("edit would corrupt the file: have %q, fix expects %q", got, e.Old)
			}
			lines[e.Line-1] = l[:e.Col-1] + e.New + l[e.EndCol-1:]
		}
	}
	os.WriteFile(filepath.Join(root, "terraform/envs/dev/main.tf"), []byte(strings.Join(lines, "\n")), 0o644)
	if err := svc.FormatFile(context.Background(), "terraform/envs/dev/main.tf"); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	svc.Run(context.Background(), "tf:terraform/envs/dev", false)
	t.Logf("second run: %s", time.Since(start))
	ds, _ = svc.Diagnostics("tf:terraform/envs/dev", "", "")
	for _, d := range ds {
		t.Errorf("expected a clean unit, still have: %+v", d)
	}
}

// Needs the registry (downloads the tiny `null` provider), so it is opt-in:
//
//	GW_TEST_NETWORK=1 go test -tags integration ./internal/checks
func TestRealProviderInitNeverWritesIntoRepo(t *testing.T) {
	if os.Getenv("GW_TEST_NETWORK") != "1" {
		t.Skip("set GW_TEST_NETWORK=1 to run (downloads a provider)")
	}
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform not installed")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "terraform/envs/dev")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`terraform {
  required_providers {
    null = {
      source  = "hashicorp/null"
      version = "~> 3.2"
    }
  }
}

resource "null_resource" "x" {
  triggers = {
    a = file("${path.module}/../../shared/data.txt")
  }
}
`), 0o644)
	os.MkdirAll(filepath.Join(root, "terraform/shared"), 0o755)
	os.WriteFile(filepath.Join(root, "terraform/shared/data.txt"), []byte("hello"), 0o644)

	rep, _ := workspace.Detect(root, nil)
	cfg := config.FromReport(rep)
	cfg.Checks.Enabled = []string{"validate"}
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.db"))
	defer st.Close()
	svc := New(root, func() *config.Config { return cfg }, OSExec{}, st, events.NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	start := time.Now()
	svc.Run(context.Background(), "tf:terraform/envs/dev", false)
	t.Logf("first run with provider download: %s", time.Since(start))
	for _, u := range svc.Status() {
		for _, r := range u.Results {
			if r.Status == "failed" {
				t.Fatalf("validate failed: %+v", r)
			}
		}
	}
	ds, _ := svc.Diagnostics("", "", "")
	for _, d := range ds {
		t.Errorf("a valid config produced %+v (file() outside the unit must resolve through the shadow tree)", d)
	}
	for _, p := range []string{".terraform", ".terraform.lock.hcl", "terraform.tfstate"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			t.Errorf("terraform wrote %s into the repo", p)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("repo dir should contain only main.tf, has %v", entries)
	}

	start = time.Now()
	svc.Run(context.Background(), "tf:terraform/envs/dev", true)
	t.Logf("forced second run (provider cached, lock kept in shadow): %s", time.Since(start))
}
