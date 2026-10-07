// Package perf times groundwork's hot paths on a generated repository the size
// of a large real one: 5,000 .tf files in 100 roots and 25 modules, plus an
// Ansible project. Run with GW_PERF=1 (make perf); budgets are deliberately
// loose so a busy CI machine doesn't flake, but catch order-of-magnitude regressions.
package perf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/api"
	"github.com/rapando/groundwork/internal/checks"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/files"
	"github.com/rapando/groundwork/internal/store"
)

const (
	stacks       = 50 // × dev/prod = 100 roots
	filesPerRoot = 40
	modules      = 25
	filesPerMod  = 40
)

func write(t *testing.T, p, body string) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func generate(t *testing.T) string {
	root := t.TempDir()
	n := 0
	for m := 0; m < modules; m++ {
		dir := filepath.Join(root, "modules", fmt.Sprintf("m%02d", m))
		write(t, filepath.Join(dir, "variables.tf"), "variable \"name\" { type = string }\nvariable \"size\" {\n  type    = number\n  default = 2\n}\n")
		write(t, filepath.Join(dir, "outputs.tf"), "output \"id\" { value = terraform_data.r0.id }\n")
		n += 2
		for f := 2; f < filesPerMod; f++ {
			write(t, filepath.Join(dir, fmt.Sprintf("r%02d.tf", f)), fmt.Sprintf(
				"resource \"terraform_data\" \"r%d\" {\n  input = \"${var.name}-%d\"\n  triggers_replace = [terraform_data.r%d.id]\n}\n", f, f, f-1))
			n++
		}
		write(t, filepath.Join(dir, "r00.tf"), "resource \"terraform_data\" \"r0\" { input = var.name }\nresource \"terraform_data\" \"r1\" { input = var.size }\n")
	}
	for s := 0; s < stacks; s++ {
		for _, env := range []string{"dev", "prod"} {
			dir := filepath.Join(root, "stacks", fmt.Sprintf("s%02d", s), "envs", env)
			write(t, filepath.Join(dir, "main.tf"), fmt.Sprintf(
				"terraform {\n  required_providers {\n    null = { source = \"hashicorp/null\" }\n  }\n}\nmodule \"a\" {\n  source = \"../../../../modules/m%02d\"\n  name   = var.name\n}\nmodule \"b\" {\n  source = \"../../../../modules/m%02d\"\n  name   = module.a.id\n}\n", s%modules, (s+1)%modules))
			write(t, filepath.Join(dir, "variables.tf"), "variable \"name\" { type = string }\nvariable \"db_password\" {\n  type      = string\n  sensitive = true\n}\n")
			write(t, filepath.Join(dir, "terraform.tfvars"), fmt.Sprintf("name = \"%s-%d\"\n", env, s))
			n += 2
			for f := 2; f < filesPerRoot; f++ {
				write(t, filepath.Join(dir, fmt.Sprintf("x%02d.tf", f)), fmt.Sprintf("resource \"terraform_data\" \"x%d\" { input = module.a.id }\n", f))
				n++
			}
		}
	}
	for g := 0; g < 40; g++ {
		write(t, filepath.Join(root, "ansible", "roles", fmt.Sprintf("r%02d", g), "tasks", "main.yml"), "---\n- name: Task\n  ansible.builtin.debug:\n    msg: hi\n")
	}
	write(t, filepath.Join(root, "ansible", "site.yml"), "---\n- hosts: all\n  roles: [r00]\n")
	write(t, filepath.Join(root, "ansible", "inventory", "dev.yml"), "all:\n  hosts:\n    h1: { ansible_connection: local }\n")
	if n < 5000 {
		t.Fatalf("generated only %d .tf files", n)
	}
	t.Logf("generated %d .tf files", n)
	return root
}

type okExec struct{}

func (okExec) LookPath(n string) (string, error) { return "/usr/bin/" + n, nil }
func (okExec) Run(_ context.Context, c checks.Cmd) (checks.Output, error) {
	return checks.Output{Stdout: []byte(`{"valid":true,"diagnostics":[],"issues":[],"errors":[]}`)}, nil
}

func timed(t *testing.T, name string, budget time.Duration, fn func()) {
	t.Helper()
	start := time.Now()
	fn()
	d := time.Since(start)
	t.Logf("%-34s %8s (budget %s)", name, d.Round(time.Millisecond), budget)
	if d > budget {
		t.Errorf("%s took %s, over its %s budget", name, d, budget)
	}
}

func TestLargeRepo(t *testing.T) {
	if os.Getenv("GW_PERF") == "" {
		t.Skip("set GW_PERF=1 (make perf)")
	}
	root := generate(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := api.New(root, "perf", events.NewBus(), st)
	t.Cleanup(a.Checks.Close) // background check runs end before the store and temp dirs go
	a.Checks.Exec = okExec{}
	h := chi.NewRouter()
	a.Routes(h)
	call := func(method, path string, body any) map[string]any {
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(b)))
		if rec.Code >= 400 {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
		}
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}

	timed(t, "detect (setup screen)", 5*time.Second, func() { call("GET", "/setup/detect", nil) })
	timed(t, "write config", 5*time.Second, func() { call("POST", "/setup/apply", map[string]any{}) })
	timed(t, "file tree (IaC only)", 2*time.Second, func() {
		if n := len(call("GET", "/files?iac_only=1", nil)["files"].([]any)); n < 5000 {
			t.Errorf("listed %d files", n)
		}
	})
	timed(t, "check status (units)", 2*time.Second, func() {
		if n := len(call("GET", "/checks", nil)["units"].([]any)); n < 125 {
			t.Errorf("%d units", n)
		}
	})
	timed(t, "run every check, tools stubbed", 60*time.Second, func() {
		if err := a.Checks.Run(context.Background(), "", true); err != nil {
			t.Fatal(err)
		}
	})
	timed(t, "re-check, nothing changed (cache)", 10*time.Second, func() { a.Checks.Run(context.Background(), "", false) })
	timed(t, "overview environments", 5*time.Second, func() { call("GET", "/envs", nil) })
	timed(t, "graph of one root", 2*time.Second, func() {
		g := call("GET", "/graph/deps?root=stacks/s07/envs/dev&env=dev", nil)["graph"].(map[string]any)
		if n := len(g["nodes"].([]any)); n < 40 {
			t.Errorf("%d nodes", n)
		}
	})
	timed(t, "variables matrix", 2*time.Second, func() { call("GET", "/vars/terraform", nil) })
	timed(t, "plaintext secret scan, whole repo", 5*time.Second, func() { call("POST", "/secrets/scan", nil) })
	timed(t, "palette files (all)", 2*time.Second, func() { call("GET", "/files", nil) })
	openFDs := func() int { e, _ := os.ReadDir("/dev/fd"); return len(e) }
	before := openFDs()
	timed(t, "file watcher start", 10*time.Second, func() {
		w, err := files.NewWatcher(root, a.Ignore(), 300*time.Millisecond, func([]string) {}, nil)
		if err != nil {
			t.Fatalf("watcher: %v", err)
		}
		defer w.Close()
		if used := openFDs() - before; used > 2000 {
			t.Errorf("the watcher holds %d file descriptors for %d files", used, 5000)
		} else {
			t.Logf("watcher holds %d file descriptors", used)
		}
		changed := make(chan []string, 1)
		w2, _ := files.NewWatcher(root, a.Ignore(), 100*time.Millisecond, func(p []string) { changed <- p }, nil)
		defer w2.Close()
		write(t, filepath.Join(root, "stacks", "s49", "envs", "prod", "x39.tf"), "resource \"terraform_data\" \"y\" {}\n")
		select {
		case <-changed:
		case <-time.After(5 * time.Second):
			t.Error("a change deep in the tree wasn't noticed")
		}
	})
}
