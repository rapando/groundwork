package scaffold

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"gopkg.in/yaml.v3"

	"github.com/rapando/groundwork/v2/internal/config"
	"github.com/rapando/groundwork/v2/internal/workspace"
)

func baseForm() Form {
	return Form{
		Project: "acme", Envs: []string{"dev", "staging", "prod"}, Layout: "dirs",
		Checks: []string{"fmt", "validate", "tflint", "ansible-lint", "yamllint"},
	}
}

func variants() map[string]Form {
	v := map[string]Form{}
	for _, layout := range []string{"dirs", "workspaces"} {
		for _, pw := range []string{"", "~/.vault-pass-acme"} {
			f := baseForm()
			f.Layout, f.VaultPasswordFile = layout, pw
			v[layout+"/vault-file="+pw] = f
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

// What setup writes is detected as the Ansible project groundwork.yaml
// describes, inventory script included. The Terraform roots don't exist until
// the user writes them.
func TestSetupRoundTripsThroughDetection(t *testing.T) {
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
			if len(written.Terraform.Roots) == 0 {
				t.Fatal("no terraform roots in groundwork.yaml")
			}
			rep, err := workspace.Detect(dir, written.Ignore)
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Ansible) != 1 || rep.Ansible[0].Path != "ansible" {
				t.Fatalf("ansible: %+v", rep.Ansible)
			}
			if inv := rep.Ansible[0].Inventories; len(inv) != 1 || inv[0] != "ansible/inventory/tf_outputs.py" {
				t.Fatalf("inventories %v", inv)
			}
			if got := written.Ansible.Projects[0].VaultPasswordFile; got != f.VaultPasswordFile {
				t.Fatalf("vault_password_file %q", got)
			}
		})
	}
}

// Only groundwork's config and the files the chosen tools need: the
// Terraform and the playbooks are the user's to write.
func TestSetupWritesOnlyTheChosenToolsFiles(t *testing.T) {
	for _, tc := range []struct {
		tools, files, checks []string
		binary               string
	}{
		{[]string{"terraform"}, []string{"groundwork.yaml"}, []string{"fmt", "validate", "tflint", "secrets"}, "terraform"},
		{[]string{"tofu"}, []string{"groundwork.yaml"}, []string{"fmt", "validate", "tflint", "secrets"}, "tofu"},
		{[]string{"ansible"}, []string{"ansible/ansible.cfg", "groundwork.yaml"}, []string{"ansible-lint", "yamllint", "syntax-check", "secrets"}, ""},
		{[]string{"tofu", "ansible"}, []string{"ansible/ansible.cfg", "ansible/inventory/tf_outputs.py", "groundwork.yaml"},
			[]string{"fmt", "validate", "tflint", "ansible-lint", "yamllint", "syntax-check", "secrets"}, "tofu"},
	} {
		t.Run(strings.Join(tc.tools, "+"), func(t *testing.T) {
			f := baseForm()
			f.Tools, f.VaultPasswordFile = tc.tools, "~/.vault-pass-acme"
			fs, err := Render(&f)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, x := range fs {
				got = append(got, x.Path)
			}
			if !reflect.DeepEqual(got, tc.files) {
				t.Fatalf("files %v", got)
			}
			c, err := config.Parse([]byte(fileNamed(t, fs, "groundwork.yaml")), config.FileName)
			if err != nil {
				t.Fatal(err)
			}
			if c.Terraform.Binary != tc.binary || (tc.binary == "") != (len(c.Terraform.Roots) == 0) {
				t.Errorf("terraform %+v", c.Terraform)
			}
			if f.Ansible() != (len(c.Ansible.Projects) == 1) {
				t.Errorf("ansible %+v", c.Ansible)
			}
			if !f.Ansible() && f.VaultPasswordFile != "" {
				t.Error("a vault password file without Ansible")
			}
			if !reflect.DeepEqual(c.Checks.Enabled, tc.checks) {
				t.Errorf("checks %v", c.Checks.Enabled)
			}
		})
	}
}

func TestDefaultTools(t *testing.T) {
	look := func(have ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			for _, h := range have {
				if h == n {
					return "/bin/" + n, nil
				}
			}
			return "", exec.ErrNotFound
		}
	}
	for want, have := range map[string][]string{"terraform": {"terraform", "tofu"}, "tofu": {"tofu"}, "": nil} {
		exp := []string{want, "ansible"}
		if want == "" {
			exp[0] = "terraform"
		}
		if got := DefaultTools(look(have...)); !reflect.DeepEqual(got, exp) {
			t.Errorf("with %v: %v", have, got)
		}
	}
}

func TestValidation(t *testing.T) {
	f := baseForm()
	f.Envs = []string{"dev", "Bad Name", "dev"}
	f.Layout = "flat"
	f.Checks = []string{"rm"}
	f.Project = ""
	f.Tools = []string{"terraform", "tofu", "pulumi"}
	errs := f.Validate()
	fields := map[string]bool{}
	for _, e := range errs {
		fields[e.Field] = true
	}
	for _, want := range []string{"envs", "layout", "checks", "project", "tools"} {
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
	os.MkdirAll(filepath.Join(dir, "ansible"), 0o755)
	os.WriteFile(filepath.Join(dir, "ansible/ansible.cfg"), []byte("mine\n"), 0o644)
	fs = append(fs, GitignoreEntry())
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
	if status["ansible/ansible.cfg"] != "skipped" || status[".gitignore"] != "merged" || status["groundwork.yaml"] != "created" {
		t.Fatalf("statuses: %v", status)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "ansible/ansible.cfg")); string(b) != "mine\n" {
		t.Fatal("ansible.cfg overwritten")
	}
	gi, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.HasPrefix(string(gi), "node_modules/\n") || !strings.Contains(string(gi), ".groundwork/") {
		t.Fatalf("gitignore not merged: %q", gi)
	}
	// explicit overwrite
	res, _ = Apply(dir, fs, map[string]bool{"ansible/ansible.cfg": true})
	for _, r := range res {
		if r.Path == "ansible/ansible.cfg" && r.Status != "overwritten" {
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
// field through as a host variable. It runs the binary setup chose.
func TestInventoryScriptPassesExtraFields(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	f := baseForm()
	f.Tools = []string{"tofu", "ansible"}
	dir := t.TempDir()
	fs, _ := Render(&f)
	if _, err := Apply(dir, fs, nil); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	out := `{"web":[{"name":"web-1","ip":"10.0.0.1","ansible_user":"ubuntu","ansible_port":2222}],` +
		`"app":[{"name":"c1","ansible_host":"demo-web","ansible_connection":"community.docker.docker","ip":null}]}`
	if err := os.WriteFile(filepath.Join(bin, "tofu"), []byte("#!/bin/sh\ncat <<'JSON'\n"+out+"\nJSON\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "terraform"), []byte("#!/bin/sh\necho terraform ran >&2; exit 1\n"), 0o755); err != nil {
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

// The vault password is typed in the console unless the form names a file,
// which then goes into groundwork.yaml.
func TestScaffoldVaultPassword(t *testing.T) {
	f := baseForm()
	files, err := Render(&f)
	if err != nil {
		t.Fatal(err)
	}
	if c := ConfigFor(&f); c.Ansible.Projects[0].VaultPasswordFile != "" {
		t.Fatal("no file unless asked")
	}
	if cfg := fileNamed(t, files, "ansible/ansible.cfg"); !strings.Contains(cfg, "Variables → Secrets → Unlock") || strings.Contains(cfg, "\nvault_password_file") {
		t.Fatalf("ansible.cfg:\n%s", cfg)
	}

	f.VaultPasswordFile = "~/.vault-pass"
	files, err = Render(&f)
	if err != nil {
		t.Fatal(err)
	}
	if c := ConfigFor(&f); c.Ansible.Projects[0].VaultPasswordFile != "~/.vault-pass" {
		t.Fatalf("%+v", c.Ansible.Projects[0])
	}
	if cfg := fileNamed(t, files, "ansible/ansible.cfg"); !strings.Contains(cfg, "~/.vault-pass") {
		t.Fatalf("ansible.cfg:\n%s", cfg)
	}
	for _, bad := range []string{"../../etc/passwd", " ~/.vault-pass", "a\nb"} {
		f.VaultPasswordFile = bad
		if errs := f.Validate(); len(errs) == 0 || errs[0].Field != "vault_password_file" {
			t.Errorf("%q accepted: %v", bad, errs)
		}
	}
}

func fileNamed(t *testing.T, files []File, p string) string {
	t.Helper()
	for _, f := range files {
		if f.Path == p {
			return f.Content
		}
	}
	t.Fatalf("no %s", p)
	return ""
}

// The password file must sit outside the repository; an existing one is used
// as it is, a new one is created with mode 0600 and never replaced.
func TestPasswordFile(t *testing.T) {
	root := t.TempDir()
	f := baseForm()
	if v, err := PasswordFile(root, &f); v != nil || err != nil {
		t.Fatalf("no file named: %v %v", v, err)
	}
	for _, inside := range []string{".vault-pass", "../.vault-pass", filepath.Join(root, "x/.vault-pass")} {
		f.VaultPasswordFile = inside
		if _, err := PasswordFile(root, &f); err == nil {
			t.Errorf("%s inside the repository accepted", inside)
		}
	}

	f.VaultPasswordFile = filepath.Join(t.TempDir(), "keys", ".vault-pass")
	v, err := PasswordFile(root, &f)
	if err != nil || v.Exists {
		t.Fatalf("%+v %v", v, err)
	}
	for _, bad := range []string{"", "a\nb"} {
		if err := v.Create(bad); err == nil {
			t.Errorf("password %q accepted", bad)
		}
	}
	if err := v.Create("s3cret"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(v.Full)
	st, _ := os.Stat(v.Full)
	if string(b) != "s3cret\n" || st.Mode().Perm() != 0o600 {
		t.Fatalf("%q %v", b, st.Mode())
	}
	if err := v.Create("other"); err == nil {
		t.Fatal("replaced an existing file")
	}
	if v, err = PasswordFile(root, &f); err != nil || !v.Exists {
		t.Fatalf("%+v %v", v, err)
	}
}
