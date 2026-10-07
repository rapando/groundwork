package scaffold

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"gopkg.in/yaml.v3"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/workspace"
)

func baseForm() Form {
	return Form{
		Preset: "aws-envs", Project: "acme", Backend: "s3", Bucket: "acme-tfstate", Region: "eu-west-1",
		Envs: []string{"dev", "staging", "prod"}, Layout: "dirs", Inventory: "terraform",
		Roles: []string{"common", "nginx"}, Vault: true,
		Checks:    []string{"fmt", "validate", "tflint", "ansible-lint", "yamllint"},
		PreCommit: true, CI: true,
	}
}

func variants() map[string]Form {
	v := map[string]Form{}
	for _, preset := range Presets {
		for _, layout := range []string{"dirs", "workspaces"} {
			for _, inv := range []string{"terraform", "static"} {
				f := baseForm()
				f.Preset, f.Layout, f.Inventory = preset, layout, inv
				if preset == "onprem" {
					f.Backend, f.Bucket, f.Region = "local", "", ""
				}
				v[preset+"/"+layout+"/"+inv] = f
			}
		}
	}
	return v
}

func TestRenderedFilesAreSyntacticallyValid(t *testing.T) {
	for name, f := range variants() {
		t.Run(name, func(t *testing.T) {
			fs, err := Render(&f)
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range fs {
				switch {
				case strings.HasSuffix(file.Path, ".tf") || strings.HasSuffix(file.Path, ".tfvars") || strings.HasSuffix(file.Path, ".hcl"):
					_, diags := hclsyntax.ParseConfig([]byte(file.Content), file.Path, hcl.InitialPos)
					if diags.HasErrors() {
						t.Errorf("%s: %v\n%s", file.Path, diags, file.Content)
					}
				case strings.HasSuffix(file.Path, ".yml") || strings.HasSuffix(file.Path, ".yaml") ||
					file.Path == ".yamllint" || file.Path == ".ansible-lint":
					var v any
					if err := yaml.Unmarshal([]byte(file.Content), &v); err != nil {
						t.Errorf("%s: %v\n%s", file.Path, err, file.Content)
					}
				}
				if strings.Contains(file.Content, "<no value>") {
					t.Errorf("%s has unresolved template value", file.Path)
				}
			}
		})
	}
}

// The scaffold, once written, must be detected exactly as the form describes:
// detection → config must agree with the config the scaffold wrote.
func TestScaffoldRoundTripsThroughDetection(t *testing.T) {
	for name, f := range variants() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			fs, err := Render(&f)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(dir, fs, nil); err != nil {
				t.Fatal(err)
			}
			written, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			rep, err := workspace.Detect(dir, written.Ignore)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Mode != "standalone" {
				t.Errorf("mode %s", rep.Mode)
			}
			if len(rep.TerraformRoots) != len(written.Terraform.Roots) {
				t.Fatalf("roots: detected %d, config has %d", len(rep.TerraformRoots), len(written.Terraform.Roots))
			}
			if len(rep.Ansible) != 1 || rep.Ansible[0].Path != "ansible" {
				t.Fatalf("ansible: %+v", rep.Ansible)
			}
			if len(rep.Ansible[0].Roles) != len(f.Roles) {
				t.Errorf("roles %v", rep.Ansible[0].Roles)
			}
			got := config.FromReport(rep)
			byPath := func(r []config.TFRoot) {
				sort.Slice(r, func(i, j int) bool { return r[i].Path < r[j].Path })
			}
			byPath(got.Terraform.Roots)
			byPath(written.Terraform.Roots)
			if !reflect.DeepEqual(got.Terraform.Roots, written.Terraform.Roots) {
				t.Errorf("roots differ\n detected %+v\n written  %+v", got.Terraform.Roots, written.Terraform.Roots)
			}
			if f.Layout == "dirs" && f.Preset == "aws-envs" && len(rep.TerraformModules) != 1 {
				t.Errorf("modules %+v", rep.TerraformModules)
			}
			if f.Inventory == "static" {
				if !reflect.DeepEqual(got.Ansible.Projects[0].Inventories, written.Ansible.Projects[0].Inventories) {
					t.Errorf("inventories differ %v vs %v", got.Ansible.Projects[0].Inventories, written.Ansible.Projects[0].Inventories)
				}
			}
		})
	}
}

func TestValidation(t *testing.T) {
	f := baseForm()
	f.Envs = []string{"dev", "Bad Name", "dev"}
	f.Region = "mars"
	f.Bucket = "X"
	f.Roles = []string{"../etc"}
	f.Project = ""
	errs := f.Validate()
	fields := map[string]bool{}
	for _, e := range errs {
		fields[e.Field] = true
	}
	for _, want := range []string{"envs", "region", "bucket", "roles", "project"} {
		if !fields[want] {
			t.Errorf("missing error for %s: %v", want, errs)
		}
	}
	if _, err := Render(&f); err == nil {
		t.Fatal("render should refuse invalid form")
	}
}

func TestApplyNeverOverwritesSilently(t *testing.T) {
	dir := t.TempDir()
	f := baseForm()
	fs, _ := Render(&f)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("mine\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\n"), 0o644)

	pl, _ := Plan(dir, fs)
	existing := 0
	for _, p := range pl {
		if p.Exists {
			existing++
		}
	}
	if existing != 2 {
		t.Fatalf("plan saw %d existing files", existing)
	}
	res, err := Apply(dir, fs, nil)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, r := range res {
		status[r.Path] = r.Status
	}
	if status["README.md"] != "skipped" || status[".gitignore"] != "merged" || status["groundwork.yaml"] != "created" {
		t.Fatalf("statuses: %v", status)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "README.md")); string(b) != "mine\n" {
		t.Fatal("README overwritten")
	}
	gi, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.HasPrefix(string(gi), "node_modules/\n") || !strings.Contains(string(gi), ".groundwork/") {
		t.Fatalf("gitignore not merged: %q", gi)
	}
	// explicit overwrite
	res, _ = Apply(dir, fs, map[string]bool{"README.md": true})
	for _, r := range res {
		if r.Path == "README.md" && r.Status != "overwritten" {
			t.Fatalf("got %s", r.Status)
		}
	}
	// second merge is a no-op
	res, _ = Apply(dir, fs, nil)
	for _, r := range res {
		if r.Path == ".gitignore" && r.Status != "skipped" {
			t.Fatalf("idempotent merge: %s", r.Status)
		}
	}
	// executable bit
	if st, _ := os.Stat(filepath.Join(dir, "ansible/inventory/tf_outputs.py")); st == nil || st.Mode()&0o100 == 0 {
		t.Fatal("inventory script not executable")
	}
}

func TestApplyRejectsEscapes(t *testing.T) {
	dir := t.TempDir()
	_, err := Apply(dir, []File{{Path: "ok.txt", Content: "x"}, {Path: "../evil", Content: "x"}}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "ok.txt")); statErr == nil {
		t.Fatal("nothing should be written when any path is invalid")
	}
}

func TestZip(t *testing.T) {
	f := baseForm()
	fs, _ := Render(&f)
	var buf bytes.Buffer
	if err := WriteZip(&buf, fs); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil || len(zr.File) != len(fs) {
		t.Fatalf("%v %d/%d", err, len(zr.File), len(fs))
	}
}

func TestDefaultThreeEnvLayoutFileCount(t *testing.T) {
	f := baseForm()
	fs, _ := Render(&f)
	t.Logf("%d files", len(fs))
	if len(fs) < 30 {
		t.Fatalf("suspiciously few files: %d", len(fs))
	}
}

func TestTerraformOutputsInventoryIsDetected(t *testing.T) {
	f := baseForm()
	dir := t.TempDir()
	fs, _ := Render(&f)
	Apply(dir, fs, nil)
	rep, _ := workspace.Detect(dir, nil)
	inv := rep.Ansible[0].Inventories
	if len(inv) != 1 || inv[0] != "ansible/inventory/tf_outputs.py" {
		t.Fatalf("inventories %v", inv)
	}
	for _, e := range rep.Envs {
		if e.Name == "tf_outputs" {
			t.Fatal("script mistaken for an environment")
		}
	}
}
