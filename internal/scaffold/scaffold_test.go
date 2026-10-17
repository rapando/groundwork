package scaffold

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
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
				switch preset {
				case "gcp-envs":
					f.Backend, f.Region, f.GCPProject = "gcs", "europe-west1", "acme-infra"
				case "docker", "onprem":
					f.Backend, f.Bucket, f.Region = "local", "", ""
				default:
					if c, ok := Clouds[preset]; ok {
						f.Backend, f.Bucket, f.Region = "local", "", c.RegionHint
					}
				}
				v[preset+"/"+layout+"/"+inv] = f
				if c, ok := Clouds[preset]; ok { // and on the provider's object storage
					g := f
					g.Backend, g.Bucket, g.S3Endpoint = "s3", "acme-tfstate", c.S3Hint
					v[preset+"/s3/"+layout+"/"+inv] = g
				}
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
			if f.Layout == "dirs" && f.Preset != "onprem" && len(rep.TerraformModules) != 1 {
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

func TestCloudsAreListedAsPresets(t *testing.T) {
	for preset := range Clouds {
		if !in(preset, Presets) {
			t.Errorf("%s is in Clouds but not in Presets", preset)
		}
	}
}

func TestCloudPresetValidation(t *testing.T) {
	f := baseForm()
	f.Preset, f.Backend, f.Bucket, f.Region = "vultr", "s3", "", "eu-west-1"
	fields := map[string]bool{}
	for _, e := range f.Validate() {
		fields[e.Field] = true
	}
	if !fields["region"] || !fields["bucket"] || !fields["s3_endpoint"] || fields["backend"] {
		t.Errorf("want region, bucket and s3_endpoint errors, got %v", f.Validate())
	}
	f.Backend, f.Region = "local", "ams"
	if errs := f.Validate(); len(errs) > 0 {
		t.Fatalf("valid vultr form rejected: %v", errs)
	}
	f.Backend, f.Bucket, f.S3Endpoint = "s3", "acme-tfstate", "http://ams1.vultrobjects.com"
	if errs := f.Validate(); len(errs) != 1 || errs[0].Field != "s3_endpoint" {
		t.Fatalf("want an s3_endpoint error for a plain-http endpoint, got %v", errs)
	}
	f.S3Endpoint = "https://ams1.vultrobjects.com"
	if errs := f.Validate(); len(errs) > 0 {
		t.Fatalf("valid vultr s3 form rejected: %v", errs)
	}
	f.Preset, f.Region, f.S3Endpoint = "docker", "", ""
	if errs := f.Validate(); len(errs) != 1 || errs[0].Field != "backend" {
		t.Fatalf("want s3 rejected for docker, got %v", errs)
	}
}

func TestS3Region(t *testing.T) {
	for in, want := range map[string]string{
		"https://fsn1.your-objectstorage.com":     "fsn1",
		"https://eu-central-1.linodeobjects.com/": "eu-central-1",
		"https://s3.example.com:9000":             "s3",
	} {
		if got := S3Region(in); got != want {
			t.Errorf("S3Region(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGCPValidation(t *testing.T) {
	f := baseForm()
	f.Preset, f.Backend, f.Region, f.GCPProject = "gcp-envs", "gcs", "eu-west-1", "X"
	fields := map[string]bool{}
	for _, e := range f.Validate() {
		fields[e.Field] = true
	}
	for _, want := range []string{"region", "gcp_project"} {
		if !fields[want] {
			t.Errorf("missing error for %s", want)
		}
	}
	f.Region, f.GCPProject = "europe-west1", "acme-infra"
	if errs := f.Validate(); len(errs) > 0 {
		t.Fatalf("valid gcp form rejected: %v", errs)
	}
	f.Backend = "s3"
	if errs := f.Validate(); len(errs) == 0 {
		t.Fatal("s3 backend accepted for gcp-envs")
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

// The inventory script turns ip into ansible_host and passes every other
// field through as a host variable.
func TestInventoryScriptPassesExtraFields(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	f := baseForm()
	dir := t.TempDir()
	fs, _ := Render(&f)
	if _, err := Apply(dir, fs, nil); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	out := `{"web":[{"name":"web-1","ip":"10.0.0.1","ansible_user":"ubuntu","ansible_port":2222}],` +
		`"app":[{"name":"c1","ansible_host":"demo-web","ansible_connection":"community.docker.docker","ip":null}]}`
	if err := os.WriteFile(filepath.Join(bin, "terraform"), []byte("#!/bin/sh\ncat <<'JSON'\n"+out+"\nJSON\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(py, filepath.Join(dir, "ansible/inventory/tf_outputs.py"), "--list")
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	var inv struct {
		Meta struct {
			Hostvars map[string]map[string]any `json:"hostvars"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(b, &inv); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	want := map[string]map[string]any{
		"web-1": {"ansible_host": "10.0.0.1", "ansible_user": "ubuntu", "ansible_port": float64(2222)},
		"c1":    {"ansible_host": "demo-web", "ansible_connection": "community.docker.docker"},
	}
	if !reflect.DeepEqual(inv.Meta.Hostvars, want) {
		t.Errorf("hostvars %v", inv.Meta.Hostvars)
	}
}
