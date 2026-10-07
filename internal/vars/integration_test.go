//go:build integration

package vars

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every non-sensitive value in the matrix must equal what `terraform console`
// evaluates for the same variable, with the same environment.
func TestMatrixMatchesTerraformConsole(t *testing.T) {
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform not installed")
	}
	src, _ := filepath.Abs("../../testdata/repos/vars-lab")
	repo := t.TempDir()
	filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(repo, rel), 0o755)
		}
		b, _ := os.ReadFile(p)
		return os.WriteFile(filepath.Join(repo, rel), b, 0o644)
	})
	environ := []string{"TF_VAR_region=ap-south-1"} // must rank between defaults and tfvars
	targets := []Target{{Root: "envs/dev", Env: "dev"}, {Root: "envs/prod", Env: "prod"}}
	m := Matrix(repo, targets, environ)
	compared := 0
	for ti, tgt := range targets {
		dir := filepath.Join(repo, tgt.Root)
		init := exec.Command("terraform", "init", "-input=false", "-no-color")
		init.Dir = dir
		if out, err := init.CombinedOutput(); err != nil {
			t.Fatalf("init: %v %s", err, out)
		}
		for _, row := range m {
			c := row.Cells[ti]
			if row.Sensitive || c.State == "missing" || c.State == "absent" {
				continue
			}
			cmd := exec.Command("terraform", "console", "-no-color")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), environ...)
			cmd.Stdin = strings.NewReader("jsonencode(var." + row.Name + ")\n")
			var out bytes.Buffer
			cmd.Stdout = &out
			if err := cmd.Run(); err != nil {
				t.Fatalf("console %s: %v", row.Name, err)
			}
			// console prints a quoted JSON string; unquote it
			var got string
			if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &got); err != nil {
				t.Fatalf("console output %q: %v", out.String(), err)
			}
			want := c.Value
			if c.State == "env" {
				want = `"ap-south-1"` // masked in the matrix; check the source is right instead
			}
			compared++
			if got != want {
				t.Errorf("%s/%s: matrix says %s (%s), terraform console says %s", tgt.Env, row.Name, c.Value, c.Source, got)
			}
		}
	}
	t.Logf("compared %d values with terraform console", compared)
	if compared < 8 {
		t.Fatalf("only %d comparisons ran", compared)
	}
}
