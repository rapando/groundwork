package scaffold

import (
	"bytes"
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"
	"text/template"

	"github.com/rapando/groundwork/internal/config"
)

//go:embed presets
var presetFS embed.FS

// File is one rendered output file.
type File struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Executable bool   `json:"executable,omitempty"`
	// Merge means: if the file exists, append only the lines it lacks
	// (used for .gitignore) instead of treating it as a conflict.
	Merge bool `json:"merge,omitempty"`
}

type tplData struct {
	*Form
	Env          string
	Index        int
	Role         string
	FirstEnv     string
	ModuleSource string
}

type entry struct {
	src, dst string
	each     string // "", "env", "role"
	exec     bool
	merge    bool
	when     func(*Form) bool
}

func always(*Form) bool { return true }

func manifest(f *Form) []entry {
	tf := "presets/" + f.Preset + "/terraform/"
	sh := "presets/shared/"
	m := []entry{
		{src: sh + "README.md.tmpl", dst: "README.md"},
		{src: sh + "gitignore.tmpl", dst: ".gitignore", merge: true},
	}
	if f.Preset == "aws-envs" {
		for _, n := range []string{"main", "variables", "outputs", "versions"} {
			m = append(m, entry{src: tf + "network_" + n + ".tf.tmpl", dst: "terraform/modules/network/" + n + ".tf"})
		}
	}
	if f.Layout == "dirs" {
		for _, n := range []string{"main.tf", "backend.tf", "variables.tf", "outputs.tf"} {
			m = append(m, entry{src: tf + n + ".tmpl", dst: "terraform/envs/{{.Env}}/" + n, each: "env"})
		}
		m = append(m, entry{src: tf + "tfvars.tmpl", dst: "terraform/envs/{{.Env}}/terraform.tfvars", each: "env"})
	} else {
		for _, n := range []string{"main.tf", "backend.tf", "variables.tf", "outputs.tf"} {
			m = append(m, entry{src: tf + n + ".tmpl", dst: "terraform/" + n})
		}
		m = append(m, entry{src: tf + "tfvars.tmpl", dst: "terraform/env/{{.Env}}.tfvars", each: "env"})
	}

	m = append(m,
		entry{src: sh + "ansible.cfg.tmpl", dst: "ansible/ansible.cfg"},
		entry{src: sh + "site.yml.tmpl", dst: "ansible/playbooks/site.yml"},
		entry{src: sh + "role_tasks.yml.tmpl", dst: "ansible/roles/{{.Role}}/tasks/main.yml", each: "role"},
		entry{src: sh + "group_vars_vars.yml.tmpl", dst: "ansible/group_vars/{{.Env}}/vars.yml", each: "env"},
	)
	if f.Vault {
		m = append(m, entry{src: sh + "group_vars_vault.yml.example.tmpl", dst: "ansible/group_vars/{{.Env}}/vault.yml.example", each: "env"})
	}
	if f.Inventory == "terraform" {
		m = append(m, entry{src: sh + "tf_outputs.py.tmpl", dst: "ansible/inventory/tf_outputs.py", exec: true})
	} else {
		m = append(m, entry{src: sh + "inventory_static.yml.tmpl", dst: "ansible/inventory/{{.Env}}.yml", each: "env"})
	}
	if f.HasCheck("tflint") {
		m = append(m, entry{src: sh + "tflint.hcl.tmpl", dst: ".tflint.hcl"})
	}
	if f.HasCheck("ansible-lint") {
		m = append(m, entry{src: sh + "ansible-lint.tmpl", dst: ".ansible-lint"})
	}
	if f.HasCheck("yamllint") {
		m = append(m, entry{src: sh + "yamllint.tmpl", dst: ".yamllint"})
	}
	if f.PreCommit {
		m = append(m, entry{src: sh + "pre-commit-config.yaml.tmpl", dst: ".pre-commit-config.yaml"})
	}
	if f.CI {
		m = append(m, entry{src: sh + "plan-workflow.yml.tmpl", dst: ".github/workflows/plan.yml"})
	}
	return m
}

// Render produces the full file set for a standalone scaffold, entirely in
// memory (this is also what the preview shows). groundwork.yaml is generated
// from the same form so the two can never disagree.
func Render(f *Form) ([]File, error) {
	f.Normalize()
	if errs := f.Validate(); len(errs) > 0 {
		return nil, errs[0]
	}
	base := tplData{Form: f, FirstEnv: f.Envs[0]}
	if f.Layout == "workspaces" {
		base.ModuleSource = "./modules/network"
	} else {
		base.ModuleSource = "../../modules/network"
	}

	var out []File
	for _, e := range manifest(f) {
		raw, err := presetFS.ReadFile(e.src)
		if err != nil {
			return nil, fmt.Errorf("preset file %s: %w", e.src, err)
		}
		tpl, err := template.New(e.src).Funcs(template.FuncMap{}).Option("missingkey=error").Parse(string(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.src, err)
		}
		var datas []tplData
		switch e.each {
		case "env":
			for i, env := range f.Envs {
				d := base
				d.Env, d.Index = env, i+1
				datas = append(datas, d)
			}
		case "role":
			for _, r := range f.Roles {
				d := base
				d.Role = r
				datas = append(datas, d)
			}
		default:
			datas = []tplData{base}
		}
		for _, d := range datas {
			dst, err := renderString(e.dst, d)
			if err != nil {
				return nil, err
			}
			var buf bytes.Buffer
			if err := tpl.Execute(&buf, d); err != nil {
				return nil, fmt.Errorf("%s: %w", e.src, err)
			}
			out = append(out, File{Path: dst, Content: buf.String(), Executable: e.exec, Merge: e.merge})
		}
	}

	cfg := ConfigFor(f)
	y, err := cfg.Marshal()
	if err != nil {
		return nil, err
	}
	if _, err := config.Parse(y, config.FileName); err != nil {
		return nil, fmt.Errorf("generated config is invalid: %w", err)
	}
	out = append(out, File{Path: config.FileName, Content: string(y)})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func renderString(s string, d tplData) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	t, err := template.New("path").Parse(s)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

// ConfigFor builds the groundwork.yaml matching a scaffold form.
func ConfigFor(f *Form) *config.Config {
	c := &config.Config{
		Version: 1, Mode: "standalone",
		Checks: config.Checks{OnSave: true},
		Ignore: []string{"vendor/", "node_modules/", "**/.terraform/"},
	}
	c.Terraform.Binary = "terraform"
	if f.Layout == "dirs" {
		for _, e := range f.Envs {
			c.Terraform.Roots = append(c.Terraform.Roots, config.TFRoot{Path: "terraform/envs/" + e, Env: e})
		}
		c.Terraform.Modules = []string{"terraform/modules/*"}
	} else {
		envs := map[string]config.TFEnv{}
		for _, e := range f.Envs {
			envs[e] = config.TFEnv{Workspace: e, VarFiles: []string{"env/" + e + ".tfvars"}}
		}
		c.Terraform.Roots = []config.TFRoot{{Path: "terraform", Envs: envs}}
		c.Terraform.Modules = []string{"terraform/modules/*"}
	}
	if f.Preset == "onprem" {
		c.Terraform.Modules = nil
	}
	ap := config.AnsibleProject{Path: "ansible", Config: "ansible.cfg"}
	if f.Inventory == "static" {
		ap.Inventories = map[string]string{}
		for _, e := range f.Envs {
			ap.Inventories[e] = "inventory/" + e + ".yml"
		}
	}
	c.Ansible.Projects = []config.AnsibleProject{ap}
	for _, name := range []string{"fmt", "validate", "tflint", "checkov", "ansible-lint", "yamllint"} {
		if f.HasCheck(name) {
			c.Checks.Enabled = append(c.Checks.Enabled, name)
		}
	}
	c.Checks.Enabled = append(c.Checks.Enabled, "syntax-check", "secrets") // a playbook is always scaffolded; secrets is built in
	return c
}

// LintReq asks for one lint config file placed in Dir ("" or "." = repo root).
type LintReq struct {
	Kind string `json:"kind"` // tflint | ansible-lint | yamllint
	Dir  string `json:"dir"`
}

// LintConfigs renders just the optional lint config files, for embedded mode
// where groundwork must not scaffold the repo itself.
func LintConfigs(f *Form, reqs []LintReq) ([]File, error) {
	names := map[string][2]string{
		"tflint":       {"presets/shared/tflint.hcl.tmpl", ".tflint.hcl"},
		"ansible-lint": {"presets/shared/ansible-lint.tmpl", ".ansible-lint"},
		"yamllint":     {"presets/shared/yamllint.tmpl", ".yamllint"},
	}
	var out []File
	for _, rq := range reqs {
		n, ok := names[rq.Kind]
		if !ok {
			return nil, fmt.Errorf("no lint config for %q", rq.Kind)
		}
		dir := path.Clean(rq.Dir)
		if rq.Dir == "" {
			dir = "."
		}
		if dir == ".." || strings.HasPrefix(dir, "../") || path.IsAbs(dir) {
			return nil, fmt.Errorf("lint config dir %q must be inside the repository", rq.Dir)
		}
		raw, err := presetFS.ReadFile(n[0])
		if err != nil {
			return nil, err
		}
		t, err := template.New(n[0]).Parse(string(raw))
		if err != nil {
			return nil, err
		}
		var b bytes.Buffer
		if err := t.Execute(&b, tplData{Form: f}); err != nil {
			return nil, err
		}
		out = append(out, File{Path: path.Join(dir, n[1]), Content: b.String()})
	}
	return out, nil
}

// GitignoreEntry is the single line embedded mode adds to the repo's .gitignore.
func GitignoreEntry() File {
	return File{Path: ".gitignore", Content: ".groundwork/\n", Merge: true}
}
